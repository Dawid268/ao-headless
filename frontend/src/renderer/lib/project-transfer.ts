import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage, getApiBaseUrl } from "./api-client";
import { baseUrlForHost, clientForHost } from "./host-clients";
import { LOCAL_HOST, type HostId } from "./hosts";

export type TransferProgress = components["schemas"]["ProjectTransferProgress"];

export type TransferPhase =
  | "idle"
  | "exporting"
  | "uploading"
  | "importing"
  | "done"
  | "failed";

export type MergedTransferStatus = {
  phase: TransferPhase;
  percent: number;
  detail: string;
  error?: string;
  projectId?: string;
  exportJobId?: string;
  importJobId?: string;
};

function hostBase(hostId: HostId | undefined): string {
  if (!hostId || hostId === LOCAL_HOST) {
    const base = getApiBaseUrl();
    if (!base) throw new Error("Local daemon is not connected");
    return base;
  }
  const base = baseUrlForHost(hostId);
  if (!base) throw new Error("Remote host is not connected");
  return base;
}

function typedClient(hostId: HostId | undefined) {
  return !hostId || hostId === LOCAL_HOST ? apiClient : clientForHost(hostId);
}

async function pollJob(
  hostId: HostId | undefined,
  jobId: string,
  signal: AbortSignal,
  onProgress: (p: TransferProgress) => void,
): Promise<TransferProgress> {
  const client = typedClient(hostId);
  for (;;) {
    if (signal.aborted) throw new DOMException("cancelled", "AbortError");
    const { data, error } = await client.GET(
      "/api/v1/project-transfer-jobs/{jobId}",
      {
        params: { path: { jobId } },
        signal,
      },
    );
    if (error || !data)
      throw new Error(apiErrorMessage(error, "Transfer job status failed"));
    onProgress(data);
    if (data.done) {
      if (data.error) throw new Error(data.error);
      return data;
    }
    await new Promise((resolve) => setTimeout(resolve, 400));
  }
}

async function downloadArtifact(
  hostId: HostId | undefined,
  jobId: string,
  signal: AbortSignal,
): Promise<ArrayBuffer> {
  const base = hostBase(hostId);
  const res = await fetch(
    `${base}/api/v1/project-transfer-jobs/${encodeURIComponent(jobId)}/artifact`,
    { signal },
  );
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new Error(text || `Download failed (${res.status})`);
  }
  return res.arrayBuffer();
}

async function startImport(
  hostId: HostId | undefined,
  body: ArrayBuffer,
  query: { destDir: string; includeSecrets: boolean; name?: string },
  signal: AbortSignal,
): Promise<string> {
  const base = hostBase(hostId);
  const params = new URLSearchParams({ destDir: query.destDir });
  if (query.includeSecrets) params.set("includeSecrets", "true");
  if (query.name) params.set("name", query.name);
  const res = await fetch(`${base}/api/v1/projects/import-jobs?${params}`, {
    method: "POST",
    headers: { "Content-Type": "application/gzip" },
    body,
    signal,
  });
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    // Old remotes lack POST /projects/import-jobs; chi matches /projects/{id}
    // with id=import-jobs and returns 405 METHOD_NOT_ALLOWED.
    if (
      res.status === 405 ||
      /METHOD_NOT_ALLOWED|method_not_allowed|POST not allowed on .+\/import-jobs/i.test(
        text,
      )
    ) {
      throw new Error(
        "Destination AO is too old for project transfer. Update that host (Portainer: re-pull ghcr.io/dawid268/ao-headless:latest), then retry.",
      );
    }
    throw new Error(text || `Import failed (${res.status})`);
  }
  const json = (await res.json()) as { jobId?: string };
  if (!json.jobId) throw new Error("Import job id missing");
  return json.jobId;
}

export async function cancelTransferJob(
  hostId: HostId | undefined,
  jobId: string,
): Promise<void> {
  await typedClient(hostId).DELETE("/api/v1/project-transfer-jobs/{jobId}", {
    params: { path: { jobId } },
  });
}

export type RunProjectTransferInput = {
  sourceHostId?: HostId;
  destHostId?: HostId;
  projectId: string;
  destDir: string;
  includeSecrets: boolean;
  displayName?: string;
  signal: AbortSignal;
  onStatus: (status: MergedTransferStatus) => void;
};

/** Export on source host, upload package to dest host, import with merged %. */
export async function runProjectTransfer(
  input: RunProjectTransferInput,
): Promise<MergedTransferStatus> {
  const {
    sourceHostId,
    destHostId,
    projectId,
    destDir,
    includeSecrets,
    displayName,
    signal,
    onStatus,
  } = input;
  let exportJobId: string | undefined;
  let importJobId: string | undefined;

  const emit = (
    partial: Partial<MergedTransferStatus> &
      Pick<MergedTransferStatus, "phase" | "percent">,
  ) => {
    onStatus({
      phase: partial.phase,
      percent: partial.percent,
      detail: partial.detail ?? "",
      error: partial.error,
      projectId: partial.projectId,
      exportJobId: partial.exportJobId ?? exportJobId,
      importJobId: partial.importJobId ?? importJobId,
    });
  };

  try {
    emit({ phase: "exporting", percent: 2, detail: "Starting export" });
    const started = await typedClient(sourceHostId).POST(
      "/api/v1/projects/{id}/export-jobs",
      {
        params: { path: { id: projectId } },
        body: { includeSecrets },
        signal,
      },
    );
    if (started.error || !started.data?.jobId) {
      throw new Error(apiErrorMessage(started.error, "Could not start export"));
    }
    exportJobId = started.data.jobId;
    emit({
      phase: "exporting",
      percent: 5,
      detail: "Packing project",
      exportJobId,
    });

    await pollJob(sourceHostId, exportJobId, signal, (p) => {
      const pct = Math.min(45, Math.round((p.percent ?? 0) * 0.45));
      emit({
        phase: "exporting",
        percent: Math.max(5, pct),
        detail: p.detail || p.phase || "Exporting",
        exportJobId,
      });
    });

    emit({
      phase: "uploading",
      percent: 48,
      detail: "Downloading package",
      exportJobId,
    });
    const artifact = await downloadArtifact(sourceHostId, exportJobId, signal);
    emit({
      phase: "uploading",
      percent: 60,
      detail: "Uploading to destination",
      exportJobId,
    });

    importJobId = await startImport(
      destHostId,
      artifact,
      {
        destDir,
        includeSecrets,
        name: displayName,
      },
      signal,
    );
    emit({
      phase: "importing",
      percent: 75,
      detail: "Importing on destination",
      exportJobId,
      importJobId,
    });

    const imported = await pollJob(destHostId, importJobId, signal, (p) => {
      const pct = 75 + Math.round((p.percent ?? 0) * 0.25);
      emit({
        phase: "importing",
        percent: Math.min(99, pct),
        detail: p.detail || p.phase || "Importing",
        exportJobId,
        importJobId,
      });
    });

    const done: MergedTransferStatus = {
      phase: "done",
      percent: 100,
      detail: "Transfer complete",
      projectId: imported.projectId,
      exportJobId,
      importJobId,
    };
    onStatus(done);
    return done;
  } catch (err) {
    const cancelled = err instanceof DOMException && err.name === "AbortError";
    const message = cancelled
      ? "cancelled"
      : err instanceof Error
        ? err.message
        : String(err);
    if (exportJobId)
      void cancelTransferJob(sourceHostId, exportJobId).catch(() => undefined);
    if (importJobId)
      void cancelTransferJob(destHostId, importJobId).catch(() => undefined);
    const failed: MergedTransferStatus = {
      phase: "failed",
      percent: 0,
      detail: cancelled ? "Cancelled" : "Transfer failed",
      error: message,
      exportJobId,
      importJobId,
    };
    onStatus(failed);
    throw err;
  }
}

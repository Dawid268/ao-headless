import * as Dialog from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import { apiClient } from "../lib/api-client";
import {
  clientForHost,
  connectedHosts,
  labelForHost,
} from "../lib/host-clients";
import { LOCAL_HOST, type HostId } from "../lib/hosts";
import { projectNavigateTarget } from "../lib/navigate-to-session";
import {
  remoteWorkspaceQueryKey,
  workspaceQueryKey,
} from "../hooks/useWorkspaceQuery";
import { useUiStore } from "../stores/ui-store";
import {
  type MergedTransferStatus,
  runProjectTransfer,
} from "../lib/project-transfer";
import { useRemoteHosts } from "../hooks/useRemoteHosts";
import { RemoteDirectoryPicker } from "./RemoteDirectoryPicker";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";

type Props = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectId: string;
  projectName: string;
  /** Host that currently owns the project; undefined/local = this machine. */
  sourceHostId?: HostId;
};

type DestChoice =
  | { kind: "local" }
  | { kind: "remote"; hostId: HostId; label: string };

export function ProjectTransferDialog({
  open,
  onOpenChange,
  projectId,
  projectName,
  sourceHostId,
}: Props) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const closeSettings = useUiStore((state) => state.closeSettings);
  const { hosts: remoteHosts } = useRemoteHosts();
  const connected = connectedHosts();
  const source =
    sourceHostId && sourceHostId !== LOCAL_HOST ? sourceHostId : undefined;

  const destinations = useMemo(() => {
    const out: DestChoice[] = [];
    if (source) out.push({ kind: "local" });
    for (const host of remoteHosts) {
      if (source && host.hostId === source) continue;
      if (!source && host.hostId === sourceHostId) continue;
      out.push({
        kind: "remote",
        hostId: host.hostId,
        label: host.label || labelForHost(host.hostId) || host.hostId,
      });
    }
    return out;
  }, [remoteHosts, source, sourceHostId]);

  const [destKey, setDestKey] = useState("");
  const [destDir, setDestDir] = useState("");
  const [includeSecrets, setIncludeSecrets] = useState(false);
  const [browseOpen, setBrowseOpen] = useState(false);
  const [status, setStatus] = useState<MergedTransferStatus | null>(null);
  const [running, setRunning] = useState(false);
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!open) {
      abortRef.current?.abort();
      abortRef.current = null;
      setRunning(false);
      setStatus(null);
      setDestDir("");
      setIncludeSecrets(false);
      setBrowseOpen(false);
      setDestKey("");
      return;
    }
    if (!destKey && destinations[0]) {
      setDestKey(
        destinations[0].kind === "local" ? "local" : destinations[0].hostId,
      );
    }
  }, [open, destinations, destKey]);

  const selected: DestChoice | undefined = destinations.find((d) =>
    d.kind === "local" ? destKey === "local" : d.hostId === destKey,
  );
  const selectedRemote =
    selected?.kind === "remote"
      ? remoteHosts.find((host) => host.hostId === selected.hostId)
      : undefined;
  const destLive =
    !selected ||
    selected.kind === "local" ||
    (selectedRemote?.status === "connected" &&
      connected.includes(selected.hostId));

  useEffect(() => {
    if (
      !open ||
      running ||
      status?.phase === "done" ||
      !selected ||
      destDir.trim()
    )
      return;
    if (selected.kind !== "remote") return;
    const slug =
      projectName
        .trim()
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-|-$/g, "") || "project";
    setDestDir(`/ao-data/projects/${slug}`);
  }, [open, running, status?.phase, selected, destDir, projectName]);

  const start = async () => {
    if (!selected || !destDir.trim() || running) return;
    const destHostId = selected.kind === "local" ? undefined : selected.hostId;
    const controller = new AbortController();
    abortRef.current = controller;
    setRunning(true);
    setStatus({
      phase: "exporting",
      percent: 0,
      detail: t("projectTransfer.starting"),
    });
    try {
      const finished = await runProjectTransfer({
        sourceHostId: source,
        destHostId,
        projectId,
        destDir: destDir.trim(),
        includeSecrets,
        displayName: projectName,
        signal: controller.signal,
        onStatus: setStatus,
      });
      const crossHost = destHostId !== source;
      if (finished.phase === "done" && crossHost) {
        const sourceClient = source ? clientForHost(source) : apiClient;
        const removed = await sourceClient.DELETE("/api/v1/projects/{id}", {
          params: { path: { id: projectId } },
        });
        if (removed.error) {
          setStatus((current) =>
            current
              ? {
                  ...current,
                  error: t("projectTransfer.leaveSourceFailed"),
                }
              : current,
          );
        }
      }
      void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
      if (source) {
        void queryClient.invalidateQueries({
          queryKey: remoteWorkspaceQueryKey(source),
        });
      }
      if (destHostId && finished.phase === "done") {
        void queryClient.invalidateQueries({
          queryKey: remoteWorkspaceQueryKey(destHostId),
        });
      }
    } catch {
      /* status already set by runProjectTransfer */
    } finally {
      setRunning(false);
      abortRef.current = null;
    }
  };

  const cancel = () => {
    abortRef.current?.abort();
  };

  const canStart = Boolean(
    selected &&
    destDir.trim() &&
    destLive &&
    !running &&
    status?.phase !== "done",
  );
  const destLabel =
    selected?.kind === "remote"
      ? selected.label
      : t("projectTransfer.thisComputer");

  const openTransferred = () => {
    const project = status?.projectId;
    if (!project || !selected) return;
    const host = selected.kind === "remote" ? selected.hostId : LOCAL_HOST;
    void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
    if (selected.kind === "remote") {
      void queryClient.invalidateQueries({
        queryKey: remoteWorkspaceQueryKey(selected.hostId),
      });
    }
    closeSettings();
    onOpenChange(false);
    void navigate(projectNavigateTarget(project, host));
  };
  const failed = status?.phase === "failed";
  const done = status?.phase === "done";

  return (
    <>
      <Dialog.Root
        open={open}
        onOpenChange={(next) => {
          if (running) return;
          onOpenChange(next);
        }}
      >
        <Dialog.Portal>
          <Dialog.Overlay className="dialog-overlay" />
          <Dialog.Content className="fixed left-1/2 top-1/2 z-overlay flex w-[min(520px,calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-lg border border-border bg-popover text-popover-foreground shadow-xl">
            <div className="flex items-center gap-3 border-b border-border px-4 py-3">
              <Dialog.Title className="min-w-0 flex-1 truncate text-balance text-lg font-semibold">
                {t("projectTransfer.title")}
              </Dialog.Title>
              <Dialog.Description className="sr-only">
                {t("projectTransfer.description")}
              </Dialog.Description>
              {!running ? (
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t("projectTransfer.close")}
                  onClick={() => onOpenChange(false)}
                >
                  <X className="size-4" aria-hidden="true" />
                </Button>
              ) : null}
            </div>

            <div className="flex flex-col gap-4 p-4">
              <p className="text-sm text-muted-foreground">
                {t("projectTransfer.subtitle", { name: projectName })}
              </p>

              {destinations.length === 0 ? (
                <p role="alert" className="text-sm text-destructive">
                  {t("projectTransfer.noDestinations")}
                </p>
              ) : (
                <label className="flex flex-col gap-1.5 text-sm">
                  <span className="font-medium">
                    {t("projectTransfer.destination")}
                  </span>
                  <select
                    className="h-9 rounded-md border border-border bg-background px-3"
                    value={destKey}
                    disabled={running || done}
                    onChange={(e) => {
                      setDestKey(e.target.value);
                      setDestDir("");
                    }}
                  >
                    {destinations.map((d) => {
                      const live =
                        d.kind === "local" ||
                        remoteHosts.find((host) => host.hostId === d.hostId)
                          ?.status === "connected";
                      return (
                        <option
                          key={d.kind === "local" ? "local" : d.hostId}
                          value={d.kind === "local" ? "local" : d.hostId}
                        >
                          {d.kind === "local"
                            ? t("projectTransfer.thisComputer")
                            : `${d.label} · ${live ? t("remote.statusLive") : t("remote.statusOffline")}`}
                        </option>
                      );
                    })}
                  </select>
                </label>
              )}

              <label className="flex flex-col gap-1.5 text-sm">
                <span className="font-medium">
                  {t("projectTransfer.destDir")}
                </span>
                <div className="flex gap-2">
                  <Input
                    value={destDir}
                    disabled={running || done || !selected}
                    onChange={(e) => setDestDir(e.target.value)}
                    placeholder="/ao-data/projects/my-project"
                    aria-label={t("projectTransfer.destDir")}
                  />
                  <Button
                    type="button"
                    variant="outline"
                    disabled={running || done || !selected}
                    onClick={() => setBrowseOpen(true)}
                  >
                    {t("projectTransfer.browse")}
                  </Button>
                </div>
                <span className="text-xs text-muted-foreground">
                  {t("projectTransfer.destHint")}
                </span>
                {selected && !destLive ? (
                  <span role="alert" className="text-xs text-destructive">
                    {t("projectTransfer.destOffline")}
                  </span>
                ) : null}
              </label>

              <label className="flex items-center gap-2 text-sm">
                <Checkbox
                  checked={includeSecrets}
                  disabled={running || done}
                  onCheckedChange={(v) => setIncludeSecrets(v === true)}
                />
                <span>{t("projectTransfer.includeSecrets")}</span>
              </label>

              <p className="text-xs text-muted-foreground">
                {t("projectTransfer.exclusions")}
              </p>

              {(running || status) && (
                <div
                  className="rounded-md border border-border bg-muted/40 p-3"
                  role="status"
                  aria-live="polite"
                >
                  <div className="mb-2 flex items-center justify-between gap-2 text-sm">
                    <span className="truncate">
                      {status?.detail || t("projectTransfer.working")}
                    </span>
                    <span className="tabular-nums">
                      {status?.percent ?? 0}%
                    </span>
                  </div>
                  <div className="h-2 overflow-hidden rounded-full bg-muted">
                    <div
                      className="h-full rounded-full bg-primary transition-[width] duration-300"
                      style={{
                        width: `${Math.max(0, Math.min(100, status?.percent ?? 0))}%`,
                      }}
                    />
                  </div>
                  {failed && status?.error ? (
                    <p role="alert" className="mt-2 text-sm text-destructive">
                      {status.error}
                    </p>
                  ) : null}
                  {done ? (
                    <div className="mt-2 flex flex-col gap-1 text-sm">
                      <p>
                        {t("projectTransfer.successOn", { host: destLabel })}
                      </p>
                      <p className="truncate font-mono text-xs text-muted-foreground">
                        {destDir}
                      </p>
                      <p className="text-xs text-muted-foreground">
                        {selected?.kind === "remote"
                          ? t("projectTransfer.successRemoteHint")
                          : t("projectTransfer.successHint")}
                      </p>
                      <p className="text-xs text-muted-foreground">
                        {t("projectTransfer.sourceRemains")}
                      </p>
                      {status?.error ? (
                        <p className="text-xs text-destructive" role="alert">
                          {status.error}
                        </p>
                      ) : null}
                    </div>
                  ) : null}
                </div>
              )}
            </div>

            <div className="flex items-center justify-end gap-2 border-t border-border px-4 py-3">
              {running ? (
                <Button type="button" variant="outline" onClick={cancel}>
                  {t("projectTransfer.cancel")}
                </Button>
              ) : (
                <Button
                  type="button"
                  variant="ghost"
                  onClick={() => onOpenChange(false)}
                >
                  {t("projectTransfer.close")}
                </Button>
              )}
              {failed ? (
                <Button
                  type="button"
                  variant="primary"
                  disabled={!canStart}
                  onClick={() => void start()}
                >
                  {t("projectTransfer.retry")}
                </Button>
              ) : done ? (
                <Button
                  type="button"
                  variant="primary"
                  disabled={!status?.projectId}
                  onClick={openTransferred}
                >
                  {selected?.kind === "remote"
                    ? t("projectTransfer.openOnHost", { host: destLabel })
                    : t("projectTransfer.openProject")}
                </Button>
              ) : (
                <Button
                  type="button"
                  variant="primary"
                  disabled={!canStart || destinations.length === 0}
                  onClick={() => void start()}
                >
                  {t("projectTransfer.start")}
                </Button>
              )}
            </div>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>

      {browseOpen && selected ? (
        <RemoteDirectoryPicker
          hostId={selected.kind === "local" ? LOCAL_HOST : selected.hostId}
          hostLabel={
            selected.kind === "local"
              ? t("projectTransfer.thisComputer")
              : selected.label
          }
          connected
          initialPath={destDir || "/ao-data/projects"}
          onChoose={(path) => {
            setDestDir(path);
            setBrowseOpen(false);
          }}
          onClose={() => setBrowseOpen(false)}
        />
      ) : null}
    </>
  );
}

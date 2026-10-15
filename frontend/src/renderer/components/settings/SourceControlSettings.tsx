import { LoaderCircle } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import { useConnectedHosts } from "../../hooks/useHostConnection";
import { apiClient, apiErrorMessage } from "../../lib/api-client";
import {
  baseUrlForHost,
  clientForHost,
  labelForHost,
} from "../../lib/host-clients";
import { LOCAL_HOST, type HostId } from "../../lib/hosts";
import { createTerminalMux, muxUrlFromApiBase } from "../../lib/terminal-mux";
import { cn } from "../../lib/utils";
import { useResolvedTheme } from "../../stores/ui-store";
import { TerminalPane } from "../TerminalPane";
import { Button } from "../ui/button";
import { SettingsOptionMenu } from "./SettingsOptionMenu";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";
import { installView, type InstallJob } from "./installState";

type SourceTool = components["schemas"]["SourceControlTool"];
type ToolId = "git" | "gh" | "glab";

type AuthTerminal = {
  hostId: HostId;
  tool: ToolId;
  handleId: string;
  title: string;
  createdAt: string;
};

const TOOLS: ToolId[] = ["git", "gh", "glab"];

function clientFor(hostId: HostId) {
  return hostId === LOCAL_HOST ? apiClient : clientForHost(hostId);
}

function toolLabelKey(
  id: ToolId,
):
  | "settings.sourceControl.git"
  | "settings.sourceControl.github"
  | "settings.sourceControl.gitlab" {
  if (id === "gh") return "settings.sourceControl.github";
  if (id === "glab") return "settings.sourceControl.gitlab";
  return "settings.sourceControl.git";
}

export function SourceControlSettings({
  titleHidden = false,
}: {
  titleHidden?: boolean;
}) {
  const { t } = useTranslation();
  const theme = useResolvedTheme();
  const connected = useConnectedHosts();
  const [hostId, setHostId] = useState<HostId>(LOCAL_HOST);
  const [tools, setTools] = useState<SourceTool[]>([]);
  const [loadError, setLoadError] = useState("");
  const [jobs, setJobs] = useState<Partial<Record<ToolId, InstallJob>>>({});
  const [starting, setStarting] = useState<ToolId | null>(null);
  const [actionError, setActionError] = useState("");
  const [terminal, setTerminal] = useState<AuthTerminal | null>(null);

  const offline = hostId !== LOCAL_HOST && !connected.includes(hostId);

  const refresh = useCallback(async () => {
    if (hostId !== LOCAL_HOST && !connected.includes(hostId)) return;
    const { data, error } = await clientFor(hostId).GET(
      "/api/v1/system/source-control",
    );
    if (error || !data) {
      setLoadError(
        apiErrorMessage(error, t("settings.sourceControl.loadFailed")),
      );
      return;
    }
    setLoadError("");
    setTools(data.tools);
  }, [connected, hostId, t]);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), 3000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  useEffect(() => {
    const pending = TOOLS.filter((id) => jobs[id]?.status === "running");
    if (pending.length === 0) return;
    const timer = window.setInterval(() => {
      void (async () => {
        for (const id of pending) {
          const { data } = await clientFor(hostId).GET(
            "/api/v1/system/install/{target}",
            {
              params: { path: { target: id } },
            },
          );
          if (!data) continue;
          setJobs((current) => ({ ...current, [id]: data }));
          if (data.status !== "running") void refresh();
        }
      })();
    }, 1500);
    return () => window.clearInterval(timer);
  }, [hostId, jobs, refresh]);

  const install = async (id: ToolId) => {
    setStarting(id);
    setActionError("");
    const { data, error } = await clientFor(hostId).POST(
      "/api/v1/system/install/{target}",
      {
        params: { path: { target: id } },
      },
    );
    setStarting(null);
    if (error || !data) {
      setActionError(
        apiErrorMessage(error, t("settings.sourceControl.installFailed")),
      );
      return;
    }
    setJobs((current) => ({ ...current, [id]: data }));
    if (data.status === "succeeded") void refresh();
  };

  const signIn = async (id: Exclude<ToolId, "git">) => {
    setActionError("");
    const client = clientFor(hostId);
    const { data, error } =
      id === "gh"
        ? await client.POST("/api/v1/system/github-auth/terminal")
        : await client.POST("/api/v1/system/gitlab-auth/terminal");
    if (error || !data?.shellTerminal) {
      setActionError(
        apiErrorMessage(error, t("settings.sourceControl.signInFailed")),
      );
      return;
    }
    setTerminal({
      hostId,
      tool: id,
      handleId: data.shellTerminal.handleId,
      title: data.shellTerminal.title,
      createdAt: data.shellTerminal.createdAt,
    });
  };

  const byId = new Map(tools.map((tool) => [tool.id, tool]));
  const remoteBase = hostId === LOCAL_HOST ? undefined : baseUrlForHost(hostId);

  return (
    <SettingsSection
      title={t("settings.sourceControl")}
      titleHidden={titleHidden}
      sectionId="source-control"
      grouped
    >
      <p className="px-3 text-pretty text-xs leading-relaxed text-settings-muted">
        {t("settings.sourceControl.hint")}
      </p>
      {connected.length > 0 || offline ? (
        <SettingsOptionMenu
          aria-label={t("remote.host")}
          value={hostId}
          options={[
            { value: LOCAL_HOST, label: t("settings.harness.thisComputer") },
            ...connected.map((id) => ({
              value: id,
              label: labelForHost(id) ?? id,
            })),
            ...(offline
              ? [{ value: hostId, label: t("remote.hostLabel", { hostId }) }]
              : []),
          ]}
          onChange={(value) => {
            setHostId(value);
            setJobs({});
            setTerminal(null);
            setActionError("");
          }}
          triggerClassName="w-fit max-w-full"
        />
      ) : null}
      {offline ? (
        <p className="px-3 text-xs text-error" role="alert">
          {t("settings.sourceControl.hostOffline")}
        </p>
      ) : null}
      {!offline && tools.length > 0
        ? TOOLS.map((id) => {
            const tool = byId.get(id);
            const installed = tool?.installed === true;
            const signedIn = tool?.signedIn === true;
            const view = installView(jobs[id], starting === id);
            const busy = view.kind === "running";
            const description = !installed
              ? t("settings.sourceControl.notInstalled")
              : id === "git" || signedIn
                ? id === "git"
                  ? t("settings.sourceControl.installed")
                  : t("settings.sourceControl.signedIn")
                : t("settings.sourceControl.notSignedIn");
            return (
              <div key={id}>
                <SettingsRow
                  label={t(toolLabelKey(id))}
                  description={description}
                >
                  <span className="mr-2 inline-flex items-center gap-1.5 text-xs text-settings-muted">
                    <span
                      aria-hidden="true"
                      className={cn(
                        "size-dot-sm shrink-0 rounded-full",
                        installed && (id === "git" || signedIn)
                          ? "bg-status-ready"
                          : "bg-status-exited",
                      )}
                    />
                    <span className="sr-only">{description}</span>
                  </span>
                  {busy ? (
                    <span
                      className="inline-flex items-center gap-1.5 text-xs text-settings-muted"
                      role="status"
                    >
                      <LoaderCircle
                        className="size-4 animate-spin"
                        aria-hidden="true"
                      />
                      {t("settings.sourceControl.installing")}
                    </span>
                  ) : !installed ? (
                    <Button
                      size="sm"
                      type="button"
                      variant="primary"
                      onClick={() => void install(id)}
                    >
                      {t("settings.sourceControl.install")}
                    </Button>
                  ) : id === "git" ? null : (
                    <Button
                      size="sm"
                      type="button"
                      variant={signedIn ? "outline" : "primary"}
                      onClick={() => void signIn(id)}
                    >
                      {t(
                        signedIn
                          ? "settings.sourceControl.signInAgain"
                          : "settings.sourceControl.signIn",
                      )}
                    </Button>
                  )}
                </SettingsRow>
                {view.kind === "manual" ? (
                  <p className="px-3 pb-2 text-xs text-settings-muted">
                    {view.reason}
                    <code className="mt-1 block overflow-x-auto font-mono text-settings-label">
                      {view.command}
                    </code>
                  </p>
                ) : null}
                {view.kind === "failed" ? (
                  <p className="px-3 pb-2 text-xs text-error" role="alert">
                    {view.reason}
                  </p>
                ) : null}
              </div>
            );
          })
        : null}
      {actionError ? (
        <p className="px-3 text-xs text-error" role="alert">
          {actionError}
        </p>
      ) : null}
      {loadError ? (
        <p className="px-3 text-xs text-error" role="alert">
          {loadError}
        </p>
      ) : null}
      {terminal &&
      terminal.hostId === hostId &&
      (hostId === LOCAL_HOST || remoteBase) ? (
        <div className="mx-3 mb-2 h-64 overflow-hidden rounded-md border border-border">
          <TerminalPane
            createMux={
              remoteBase
                ? () => createTerminalMux(muxUrlFromApiBase(remoteBase))
                : undefined
            }
            daemonReady
            fontSize={12}
            terminalTarget={{
              kind: "shell",
              handleId: terminal.handleId,
              generation: terminal.createdAt,
              title: terminal.title,
            }}
            theme={theme}
          />
        </div>
      ) : null}
    </SettingsSection>
  );
}

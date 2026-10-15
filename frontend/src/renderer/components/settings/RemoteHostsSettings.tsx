import { Pencil, Server, Trash2 } from "lucide-react";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { aoBridge } from "../../lib/bridge";
import {
  baseUrlForHost,
  clientForHost,
  disconnectHost,
} from "../../lib/host-clients";
import { createTerminalMux, muxUrlFromApiBase } from "../../lib/terminal-mux";
import { TerminalPane } from "../TerminalPane";
import {
  requestRemoteHostsRefresh,
  useRemoteHosts,
} from "../../hooks/useRemoteHosts";
import { cn } from "../../lib/utils";
import { useCloudLocalAuth } from "../../hooks/useCloudLocalAuth";
import { useCloudSession } from "../../lib/cloud-session";
import {
  deleteAccountRemoteHost,
  saveAccountRemoteHost,
} from "../../lib/account-remote-hosts";
import { useSettings } from "../../hooks/useSettings";
import { useLocalSignInDialogStore } from "../../stores/local-signin-dialog-store";
import { useResolvedTheme, useUiStore } from "../../stores/ui-store";
import { ConfirmDialog } from "../ConfirmDialog";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Switch } from "../ui/switch";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

type SavedHost = { hostId: string; label: string; url: string };

export function RemoteHostsSettings({
  titleHidden,
}: {
  titleHidden?: boolean;
}) {
  const { t } = useTranslation();
  const theme = useResolvedTheme();
  const enabled = useUiStore((state) => state.remoteHosts);
  const setEnabled = useUiStore((state) => state.setRemoteHosts);
  const { status, signIn } = useCloudSession();
  const { settings } = useSettings();
  const cloudBaseUrl = settings?.cloudControlPlaneUrl ?? "";
  const { available: localAuthAvailable } = useCloudLocalAuth();
  const openLocalSignIn = useLocalSignInDialogStore(
    (state) => state.openDialog,
  );
  const [saved, setSaved] = useState<SavedHost[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [label, setLabel] = useState("");
  const [url, setUrl] = useState("");
  const [password, setPassword] = useState("");
  const [editing, setEditing] = useState<SavedHost | null>(null);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState("");
  const [confirmingRemoval, setConfirmingRemoval] = useState<SavedHost | null>(
    null,
  );
  const [removeBusy, setRemoveBusy] = useState(false);
  const [removeError, setRemoveError] = useState("");
  const formRef = useRef<HTMLFormElement>(null);
  const { hosts: liveHosts } = useRemoteHosts();
  const [drainNote, setDrainNote] = useState<Record<string, string>>({});
  const [scmNote, setScmNote] = useState("");
  const [gitlabToken, setGitlabToken] = useState("");
  const [gitHubTerminal, setGitHubTerminal] = useState<{
    hostId: string;
    handleId: string;
    title: string;
    createdAt: string;
  } | null>(null);

  const load = async () => {
    setLoading(true);
    setLoadError("");
    try {
      setSaved(await aoBridge.remotes.list());
    } catch (cause) {
      setLoadError(
        cause instanceof Error ? cause.message : t("remote.loadHostsFailed"),
      );
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (status === "authenticated") void load();
    else setSaved([]);
    // The bridge is fixed for the lifetime of this Settings page.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status]);

  const resetForm = () => {
    setEditing(null);
    setLabel("");
    setUrl("");
    setPassword("");
    setFormError("");
  };

  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const nextLabel = label.trim();
    const nextUrl = url.trim();
    if (!nextLabel || !nextUrl || (!editing?.hostId && !password)) return;
    setBusy(true);
    setFormError("");
    try {
      // Old saved records have no identity, so re-pair them through add.
      const health = editing?.hostId
        ? await aoBridge.remotes.update(editing.url, {
            label: nextLabel,
            url: nextUrl,
            ...(password ? { password } : {}),
          })
        : await aoBridge.remotes.add({
            label: nextLabel,
            url: nextUrl,
            password,
          });
      if (health === "offline") throw new Error(t("remote.hostOffline"));
      if (health === "unauthorized")
        throw new Error(t("remote.hostUnauthorized"));
      if (health === "not-a-daemon") throw new Error(t("remote.hostNotDaemon"));
      if (health === "incompatible")
        throw new Error(t("remote.hostIncompatible"));
      if (editing?.hostId) await disconnectHost(editing.hostId);
      if (editing && !editing.hostId && editing.url !== nextUrl)
        await aoBridge.remotes.remove(editing.url);
      const current = (await aoBridge.remotes.list()).find(
        (item) => item.url === nextUrl,
      );
      if (current?.hostId && cloudBaseUrl) {
        const token = await aoBridge.remotes.issueAccountToken(current.url);
        await saveAccountRemoteHost(cloudBaseUrl, {
          hostId: current.hostId,
          label: current.label,
          url: current.url,
          token,
        });
      }
      await load();
      resetForm();
      if (!enabled) setEnabled(true);
      requestRemoteHostsRefresh();
    } catch (cause) {
      setFormError(
        cause instanceof Error ? cause.message : t("remote.addHostFailed"),
      );
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!confirmingRemoval) return;
    setRemoveBusy(true);
    setRemoveError("");
    try {
      if (confirmingRemoval.hostId && cloudBaseUrl)
        await deleteAccountRemoteHost(cloudBaseUrl, confirmingRemoval.hostId);
      await aoBridge.remotes.remove(confirmingRemoval.url);
      if (confirmingRemoval.hostId)
        await disconnectHost(confirmingRemoval.hostId);
      await load();
      setConfirmingRemoval(null);
      requestRemoteHostsRefresh();
    } catch (cause) {
      setRemoveError(
        cause instanceof Error ? cause.message : t("remote.removeHostFailed"),
      );
    } finally {
      setRemoveBusy(false);
    }
  };

  if (status !== "authenticated")
    return (
      <SettingsSection
        title={t("settings.remoteHosts")}
        sectionId="remoteHosts"
        titleHidden={titleHidden}
        grouped
      >
        <SettingsRow label={t("shell.signInToAOCloud")}>
          {status === "unauthenticated" ? (
            <Button
              onClick={() =>
                localAuthAvailable ? openLocalSignIn() : signIn()
              }
              size="sm"
              type="button"
            >
              {t("shell.signInToAOCloud")}
            </Button>
          ) : null}
        </SettingsRow>
      </SettingsSection>
    );

  return (
    <>
      <SettingsSection
        title={t("settings.remoteHosts")}
        sectionId="remoteHosts"
        titleHidden={titleHidden}
        grouped
      >
        <SettingsRow
          label={t("remote.enable")}
          description={t("remote.enableDescription")}
        >
          <Switch
            aria-label={t("remote.enable")}
            checked={enabled}
            onCheckedChange={setEnabled}
          />
        </SettingsRow>
        <p className="px-3 text-pretty text-xs leading-relaxed text-settings-muted">
          {t("remote.connectionDescription")}
        </p>
      </SettingsSection>

      <SettingsSection title={t("remote.savedHosts")} grouped>
        {loading && saved.length === 0 ? (
          <p className="px-3 py-2 text-xs text-settings-muted" role="status">
            {t("remote.loadingHosts")}
          </p>
        ) : null}
        {!loading && saved.length === 0 && !loadError ? (
          <p className="px-3 py-2 text-pretty text-xs text-settings-muted">
            {t("remote.emptyHosts")}
          </p>
        ) : null}
        {saved.map((host) => {
          const live = liveHosts.find(
            (item) => item.hostId === host.hostId || item.url === host.url,
          );
          const liveStatus = live?.status ?? "offline";
          const statusLabel =
            liveStatus === "connected"
              ? t("remote.statusLive")
              : liveStatus === "connecting"
                ? t("remote.statusConnecting")
                : t("remote.statusOffline");
          return (
            <div key={host.url}>
              <SettingsRow
                icon={Server}
                label={host.label}
                description={`${statusLabel} · ${host.url}`}
              >
                <span
                  aria-hidden="true"
                  className={cn(
                    "size-dot-sm shrink-0 rounded-full",
                    liveStatus === "connected"
                      ? "bg-status-ready"
                      : liveStatus === "connecting"
                        ? "animate-status-pulse bg-status-working"
                        : "bg-muted-foreground",
                  )}
                />
                <div className="flex items-center gap-1">
                  <Button
                    aria-label={t("remote.editHost", { label: host.label })}
                    disabled={busy || removeBusy}
                    onClick={() => {
                      setEditing(host);
                      setLabel(host.label);
                      setUrl(host.url);
                      setPassword("");
                      setFormError("");
                      formRef.current?.scrollIntoView?.({ block: "nearest" });
                    }}
                    size="icon-sm"
                    type="button"
                    variant="ghost"
                  >
                    <Pencil aria-hidden="true" className="size-icon-base" />
                  </Button>
                  <Button
                    aria-label={t("remote.removeHost", { label: host.label })}
                    disabled={busy || removeBusy}
                    onClick={() => {
                      setConfirmingRemoval(host);
                      setRemoveError("");
                    }}
                    size="icon-sm"
                    type="button"
                    variant="ghost"
                  >
                    <Trash2 aria-hidden="true" className="size-icon-base" />
                  </Button>
                </div>
              </SettingsRow>
              {host.hostId && liveStatus === "connected" ? (
                <>
                  <div className="flex flex-wrap items-center justify-end gap-2 px-3 pb-2">
                    <Button
                      disabled={busy}
                      size="sm"
                      type="button"
                      variant="outline"
                      onClick={() =>
                        void (async () => {
                          setScmNote("");
                          try {
                            const { data, error } = await clientForHost(
                              host.hostId,
                            ).POST("/api/v1/system/github-auth/terminal");
                            if (error || !data?.shellTerminal)
                              throw new Error(t("remote.githubSignInFailed"));
                            setGitHubTerminal({
                              hostId: host.hostId,
                              handleId: data.shellTerminal.handleId,
                              title: data.shellTerminal.title,
                              createdAt: data.shellTerminal.createdAt,
                            });
                            setScmNote(
                              t("remote.githubSignInStarted", {
                                host: host.label,
                              }),
                            );
                          } catch (cause) {
                            setScmNote(
                              cause instanceof Error
                                ? cause.message
                                : t("remote.githubSignInFailed"),
                            );
                          }
                        })()
                      }
                    >
                      {t("remote.signInGitHub")}
                    </Button>
                    <Button
                      disabled={busy}
                      size="sm"
                      type="button"
                      variant="outline"
                      onClick={() =>
                        void (async () => {
                          setDrainNote((current) => ({
                            ...current,
                            [host.hostId]: t("remote.preparingUpdate"),
                          }));
                          try {
                            const { data, error } = await clientForHost(
                              host.hostId,
                            ).POST("/api/v1/system/update-drain");
                            if (error)
                              throw new Error(t("remote.prepareUpdateFailed"));
                            setDrainNote((current) => ({
                              ...current,
                              [host.hostId]: data?.safeToReplace
                                ? t("remote.safeToRepull")
                                : t("remote.preparingUpdate"),
                            }));
                          } catch (cause) {
                            setDrainNote((current) => ({
                              ...current,
                              [host.hostId]:
                                cause instanceof Error
                                  ? cause.message
                                  : t("remote.prepareUpdateFailed"),
                            }));
                          }
                        })()
                      }
                    >
                      {t("remote.prepareUpdate")}
                    </Button>
                  </div>
                  <form
                    className="flex items-center justify-end gap-2 px-3 pb-2"
                    onSubmit={(event) => {
                      event.preventDefault();
                      const token = gitlabToken.trim();
                      if (!token) return;
                      void (async () => {
                        setScmNote("");
                        try {
                          const { error } = await clientForHost(
                            host.hostId,
                          ).PUT("/api/v1/system/gitlab-token", {
                            body: { token },
                          });
                          if (error)
                            throw new Error(t("remote.gitLabSaveFailed"));
                          setGitlabToken("");
                          setScmNote(
                            t("remote.gitLabSaved", { host: host.label }),
                          );
                        } catch (cause) {
                          setScmNote(
                            cause instanceof Error
                              ? cause.message
                              : t("remote.gitLabSaveFailed"),
                          );
                        }
                      })();
                    }}
                  >
                    <Input
                      aria-label={t("remote.gitLabToken")}
                      autoComplete="off"
                      className="max-w-xs"
                      onChange={(event) => setGitlabToken(event.target.value)}
                      placeholder={t("remote.gitLabToken")}
                      type="password"
                      value={gitlabToken}
                    />
                    <Button
                      disabled={!gitlabToken.trim()}
                      size="sm"
                      type="submit"
                      variant="outline"
                    >
                      {t("remote.saveGitLab")}
                    </Button>
                  </form>
                  {gitHubTerminal?.hostId === host.hostId &&
                  baseUrlForHost(host.hostId) ? (
                    <div className="mx-3 mb-2 h-40 overflow-hidden rounded-md border border-border">
                      <TerminalPane
                        createMux={() =>
                          createTerminalMux(
                            muxUrlFromApiBase(
                              baseUrlForHost(host.hostId) ?? "",
                            ),
                          )
                        }
                        daemonReady
                        fontSize={12}
                        terminalTarget={{
                          kind: "shell",
                          handleId: gitHubTerminal.handleId,
                          generation: gitHubTerminal.createdAt,
                          title: gitHubTerminal.title,
                        }}
                        theme={theme}
                      />
                    </div>
                  ) : null}
                </>
              ) : null}
              {drainNote[host.hostId] ? (
                <p className="px-3 pb-2 text-xs text-settings-muted">
                  {drainNote[host.hostId]} {t("remote.prepareUpdateHint")}
                </p>
              ) : null}
              {!host.hostId ? (
                <p className="px-3 py-1 text-xs text-settings-muted">
                  {t("remote.repairRequired")}
                </p>
              ) : null}
            </div>
          );
        })}
        {scmNote ? (
          <p className="px-3 py-1 text-xs text-settings-muted" role="status">
            {scmNote}
          </p>
        ) : null}
        {loadError ? (
          <div
            className="flex items-center gap-2 px-3 py-2 text-xs text-error"
            role="alert"
          >
            {loadError}
            <Button
              onClick={() => void load()}
              size="sm"
              type="button"
              variant="outline"
            >
              {t("notify.retry")}
            </Button>
          </div>
        ) : null}
      </SettingsSection>

      <SettingsSection
        title={
          editing
            ? t("remote.editHost", { label: editing.label })
            : t("remote.addHost")
        }
        grouped
      >
        <p className="px-3 text-pretty text-xs leading-relaxed text-settings-muted">
          {t("remote.setupHint")}
        </p>
        <form
          className="flex flex-col gap-1.5"
          onSubmit={(event) => void save(event)}
          ref={formRef}
        >
          <SettingsRow label={t("remote.name")}>
            <Input
              aria-label={t("remote.name")}
              className="max-w-sm"
              disabled={busy}
              maxLength={64}
              onChange={(event) => setLabel(event.target.value)}
              required
              value={label}
            />
          </SettingsRow>
          <SettingsRow label={t("mobile.address")}>
            <Input
              aria-label={t("mobile.address")}
              className="max-w-sm"
              disabled={busy}
              inputMode="url"
              onChange={(event) => setUrl(event.target.value)}
              placeholder={t("remote.addressPlaceholder")}
              required
              value={url}
            />
          </SettingsRow>
          <SettingsRow label={t("remote.connectionPassword")}>
            <Input
              aria-label={t("remote.connectionPassword")}
              autoComplete="off"
              className="max-w-sm"
              disabled={busy}
              onChange={(event) => setPassword(event.target.value)}
              placeholder={
                editing?.hostId ? t("remote.keepPassword") : undefined
              }
              required={!editing?.hostId}
              type="password"
              value={password}
            />
          </SettingsRow>
          {formError ? (
            <p className="px-3 text-xs text-error" role="alert">
              {formError}
            </p>
          ) : null}
          <div className="flex justify-end gap-2 px-3 pt-1">
            {editing ? (
              <Button
                disabled={busy}
                onClick={resetForm}
                size="sm"
                type="button"
                variant="outline"
              >
                {t("confirm.cancel")}
              </Button>
            ) : null}
            <Button
              disabled={
                busy ||
                !label.trim() ||
                !url.trim() ||
                (!editing?.hostId && !password)
              }
              size="sm"
              type="submit"
            >
              {busy
                ? t("remote.saving")
                : editing
                  ? t("remote.saveHost")
                  : t("remote.addHost")}
            </Button>
          </div>
        </form>
      </SettingsSection>

      <ConfirmDialog
        busy={removeBusy}
        confirmLabel={t("shell.remove")}
        description={t("remote.removeDescription")}
        destructive
        error={removeError || null}
        onConfirm={() => void remove()}
        onOpenChange={(open) => {
          if (!open && !removeBusy) setConfirmingRemoval(null);
        }}
        open={confirmingRemoval !== null}
        title={t("remote.removeTitle", { label: confirmingRemoval?.label })}
      />
    </>
  );
}

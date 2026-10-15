import * as Dialog from "@radix-ui/react-dialog";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowUp,
  ChevronRight,
  Folder,
  FolderGit2,
  FolderPlus,
  X,
} from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { clientForHost } from "../lib/host-clients";
import { LOCAL_HOST, type HostId } from "../lib/hosts";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Skeleton } from "./ui/skeleton";

function fsClient(hostId: HostId) {
  return hostId === LOCAL_HOST ? apiClient : clientForHost(hostId);
}

/** Browse (and optionally create) folders on a local or remote daemon host. */
export function RemoteDirectoryPicker({
  hostId,
  hostLabel,
  connected = true,
  initialPath = "",
  onChoose,
  onClose,
}: {
  hostId: HostId;
  hostLabel: string;
  connected?: boolean;
  initialPath?: string;
  onChoose: (path: string) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [path, setPath] = useState(initialPath);
  const [lookup, setLookup] = useState(initialPath);
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState("");
  const [createError, setCreateError] = useState<string | null>(null);
  const [creatingBusy, setCreatingBusy] = useState(false);

  const directory = useQuery({
    queryKey: ["remote-fs-dirs", hostId, lookup],
    enabled: connected,
    retry: false,
    queryFn: async () => {
      const { data, error } = await fsClient(hostId).GET("/api/v1/fs/dirs", {
        params: { query: lookup ? { path: lookup } : {} },
      });
      if (error || !data)
        throw new Error(
          apiErrorMessage(error, t("remote.directoryPicker.listFailed")),
        );
      return data;
    },
  });

  const createFolder = async () => {
    const parent = directory.data?.path;
    const name = newName.trim();
    if (!parent || !name || creatingBusy) return;
    if (
      name.includes("/") ||
      name.includes("\\") ||
      name === "." ||
      name === ".."
    ) {
      setCreateError(t("remote.directoryPicker.invalidName"));
      return;
    }
    const full = `${parent.replace(/\/+$/, "")}/${name}`;
    setCreatingBusy(true);
    setCreateError(null);
    try {
      const { data, error } = await fsClient(hostId).POST("/api/v1/fs/dirs", {
        body: { path: full },
      });
      if (error || !data)
        throw new Error(
          apiErrorMessage(error, t("remote.directoryPicker.createFailed")),
        );
      setCreating(false);
      setNewName("");
      setPath(data.path);
      setLookup(data.path);
      await queryClient.invalidateQueries({
        queryKey: ["remote-fs-dirs", hostId],
      });
    } catch (err) {
      setCreateError(
        err instanceof Error
          ? err.message
          : t("remote.directoryPicker.createFailed"),
      );
    } finally {
      setCreatingBusy(false);
    }
  };

  return (
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content className="fixed left-1/2 top-1/2 z-overlay flex max-h-[min(640px,calc(100dvh-24px))] w-[min(560px,calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-lg border border-border bg-popover text-popover-foreground shadow-xl">
          <div className="flex items-center gap-3 border-b border-border px-4 py-3">
            <Dialog.Title className="min-w-0 flex-1 truncate text-balance text-lg font-semibold">
              {t("remote.directoryPicker.title", { host: hostLabel })}
            </Dialog.Title>
            <Dialog.Description className="sr-only">
              {t("remote.directoryPicker.description")}
            </Dialog.Description>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={t("remote.directoryPicker.close")}
              onClick={onClose}
            >
              <X className="size-4" aria-hidden="true" />
            </Button>
          </div>
          <form
            className="flex gap-2 p-4"
            onSubmit={(event) => {
              event.preventDefault();
              setLookup(path.trim());
            }}
          >
            <Input
              aria-label={t("remote.directoryPicker.path")}
              value={path}
              onChange={(event) => setPath(event.target.value)}
              disabled={!connected}
              placeholder="/ao-data/projects"
            />
            <Button type="submit" variant="outline" disabled={!connected}>
              {t("remote.directoryPicker.go")}
            </Button>
          </form>
          <div className="flex items-center gap-2 border-y border-border px-4 py-2">
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={t("remote.directoryPicker.parent")}
              disabled={
                !connected ||
                !directory.data ||
                directory.data.parent === directory.data.path
              }
              onClick={() => {
                if (directory.data) {
                  setPath(directory.data.parent);
                  setLookup(directory.data.parent);
                }
              }}
            >
              <ArrowUp className="size-4" aria-hidden="true" />
            </Button>
            <span
              className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground"
              title={directory.data?.path}
            >
              {directory.data?.path ?? t("remote.directoryPicker.current")}
            </span>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={!connected || !directory.data || creatingBusy}
              onClick={() => {
                setCreating((v) => !v);
                setCreateError(null);
                setNewName("");
              }}
            >
              <FolderPlus className="size-4" aria-hidden="true" />
              {t("remote.directoryPicker.newFolder")}
            </Button>
            <Button
              type="button"
              variant="primary"
              size="sm"
              disabled={!connected || !directory.data}
              onClick={() => directory.data && onChoose(directory.data.path)}
            >
              {t("remote.directoryPicker.use")}
            </Button>
          </div>
          {creating ? (
            <form
              className="flex flex-col gap-2 border-b border-border px-4 py-3"
              onSubmit={(event) => {
                event.preventDefault();
                void createFolder();
              }}
            >
              <label className="flex flex-col gap-1.5 text-sm">
                <span className="font-medium">
                  {t("remote.directoryPicker.newFolderName")}
                </span>
                <div className="flex gap-2">
                  <Input
                    autoFocus
                    value={newName}
                    disabled={creatingBusy}
                    onChange={(event) => setNewName(event.target.value)}
                    placeholder="my-project"
                    aria-label={t("remote.directoryPicker.newFolderName")}
                  />
                  <Button
                    type="submit"
                    variant="primary"
                    disabled={!newName.trim() || creatingBusy}
                  >
                    {t("remote.directoryPicker.create")}
                  </Button>
                </div>
              </label>
              {createError ? (
                <p role="alert" className="text-sm text-destructive">
                  {createError}
                </p>
              ) : null}
            </form>
          ) : null}
          <div className="min-h-20 overflow-y-auto p-2">
            {!connected ? (
              <p role="alert" className="p-3 text-sm text-destructive">
                {t("remote.directoryPicker.connect", { host: hostLabel })}
              </p>
            ) : directory.isPending ? (
              <div
                role="status"
                aria-label={t("remote.directoryPicker.loading")}
                className="space-y-2 p-2"
              >
                <Skeleton className="h-7 w-full" />
                <Skeleton className="h-7 w-4/5" />
              </div>
            ) : directory.isError ? (
              <div className="flex flex-col gap-2 p-3">
                <div className="flex items-center justify-between gap-2">
                  <p role="alert" className="text-sm text-destructive">
                    {directory.error.message}
                  </p>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => void directory.refetch()}
                  >
                    {t("remote.directoryPicker.retry")}
                  </Button>
                </div>
                {path.trim() ? (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={creatingBusy}
                    onClick={() => {
                      void (async () => {
                        setCreatingBusy(true);
                        setCreateError(null);
                        try {
                          const target = path.trim();
                          const { data, error } = await fsClient(hostId).POST(
                            "/api/v1/fs/dirs",
                            { body: { path: target } },
                          );
                          if (error || !data)
                            throw new Error(
                              apiErrorMessage(
                                error,
                                t("remote.directoryPicker.createFailed"),
                              ),
                            );
                          setLookup(data.path);
                          setPath(data.path);
                        } catch (err) {
                          setCreateError(
                            err instanceof Error
                              ? err.message
                              : t("remote.directoryPicker.createFailed"),
                          );
                        } finally {
                          setCreatingBusy(false);
                        }
                      })();
                    }}
                  >
                    {t("remote.directoryPicker.createPath")}
                  </Button>
                ) : null}
                {createError ? (
                  <p role="alert" className="text-sm text-destructive">
                    {createError}
                  </p>
                ) : null}
              </div>
            ) : directory.data?.entries.length ? (
              directory.data.entries.map((entry) => (
                <button
                  key={entry.path}
                  type="button"
                  className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-muted focus-visible:outline-2 focus-visible:outline-primary"
                  onClick={() => {
                    setPath(entry.path);
                    setLookup(entry.path);
                  }}
                >
                  {entry.gitRepo ? (
                    <FolderGit2
                      className="size-4 shrink-0 text-muted-foreground"
                      aria-hidden="true"
                    />
                  ) : (
                    <Folder
                      className="size-4 shrink-0 text-muted-foreground"
                      aria-hidden="true"
                    />
                  )}
                  <span className="min-w-0 flex-1 truncate">{entry.name}</span>
                  <ChevronRight
                    className="size-4 shrink-0 text-muted-foreground"
                    aria-hidden="true"
                  />
                </button>
              ))
            ) : (
              <p className="p-3 text-sm text-muted-foreground">
                {t("remote.directoryPicker.empty")}
              </p>
            )}
            {directory.data?.truncated ? (
              <p className="px-2 py-1 text-xs text-muted-foreground">
                {t("remote.directoryPicker.truncated")}
              </p>
            ) : null}
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

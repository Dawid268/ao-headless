import * as Dialog from "@radix-ui/react-dialog";
import { useQuery } from "@tanstack/react-query";
import { ArrowUp, ChevronRight, Folder, FolderGit2, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import {
	clientForHost,
	connectedHosts,
	labelForHost,
} from "../lib/host-clients";
import { LOCAL_HOST, type HostId } from "../lib/hosts";
import {
	type MergedTransferStatus,
	runProjectTransfer,
} from "../lib/project-transfer";
import { useRemoteHosts } from "../hooks/useRemoteHosts";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";
import { Skeleton } from "./ui/skeleton";

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
	const { hosts: remoteHosts } = useRemoteHosts();
	const connected = connectedHosts();
	const source =
		sourceHostId && sourceHostId !== LOCAL_HOST ? sourceHostId : undefined;

	const destinations = useMemo(() => {
		const out: DestChoice[] = [];
		if (source) out.push({ kind: "local" });
		for (const host of remoteHosts) {
			if (host.status !== "connected" || !connected.includes(host.hostId))
				continue;
			if (source && host.hostId === source) continue;
			if (!source && host.hostId === sourceHostId) continue;
			out.push({
				kind: "remote",
				hostId: host.hostId,
				label: host.label || labelForHost(host.hostId) || host.hostId,
			});
		}
		return out;
	}, [connected, remoteHosts, source, sourceHostId]);

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
			await runProjectTransfer({
				sourceHostId: source,
				destHostId,
				projectId,
				destDir: destDir.trim(),
				includeSecrets,
				displayName: projectName,
				signal: controller.signal,
				onStatus: setStatus,
			});
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
		selected && destDir.trim() && !running && status?.phase !== "done",
	);
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
										{destinations.map((d) => (
											<option
												key={d.kind === "local" ? "local" : d.hostId}
												value={d.kind === "local" ? "local" : d.hostId}
											>
												{d.kind === "local"
													? t("projectTransfer.thisComputer")
													: d.label}
											</option>
										))}
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
										placeholder="/home/you/code/project"
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
										<p className="mt-2 text-sm text-muted-foreground">
											{t("projectTransfer.successHint")}
										</p>
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
									onClick={() => onOpenChange(false)}
								>
									{t("projectTransfer.done")}
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
				<TransferDirectoryPicker
					hostId={selected.kind === "local" ? LOCAL_HOST : selected.hostId}
					hostLabel={
						selected.kind === "local"
							? t("projectTransfer.thisComputer")
							: selected.label
					}
					initialPath={destDir}
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

function TransferDirectoryPicker({
	hostId,
	hostLabel,
	initialPath = "",
	onChoose,
	onClose,
}: {
	hostId: HostId;
	hostLabel: string;
	initialPath?: string;
	onChoose: (path: string) => void;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const [path, setPath] = useState(initialPath);
	const [lookup, setLookup] = useState(initialPath);
	const remote = hostId !== LOCAL_HOST;
	const directory = useQuery({
		queryKey: ["transfer-fs-dirs", hostId, lookup],
		retry: false,
		queryFn: async () => {
			const client = remote ? clientForHost(hostId) : apiClient;
			const { data, error } = await client.GET("/api/v1/fs/dirs", {
				params: { query: lookup ? { path: lookup } : {} },
			});
			if (error || !data)
				throw new Error(
					apiErrorMessage(error, t("remote.directoryPicker.listFailed")),
				);
			return data;
		},
	});

	return (
		<Dialog.Root
			open
			onOpenChange={(next) => {
				if (!next) onClose();
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
							placeholder="/home/you/code"
						/>
						<Button type="submit" variant="outline">
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
								!directory.data || directory.data.parent === directory.data.path
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
							variant="primary"
							size="sm"
							disabled={!directory.data}
							onClick={() => directory.data && onChoose(directory.data.path)}
						>
							{t("remote.directoryPicker.use")}
						</Button>
					</div>
					<div className="min-h-20 overflow-y-auto p-2">
						{directory.isPending ? (
							<div
								role="status"
								aria-label={t("remote.directoryPicker.loading")}
								className="space-y-2 p-2"
							>
								<Skeleton className="h-7 w-full" />
								<Skeleton className="h-7 w-4/5" />
							</div>
						) : directory.isError ? (
							<div className="flex items-center justify-between gap-2 p-3">
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
					</div>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}

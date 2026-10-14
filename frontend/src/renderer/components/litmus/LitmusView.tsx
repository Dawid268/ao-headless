import { useState, type FormEvent } from "react";
import { FlaskConical, RotateCw } from "lucide-react";
import { ScrollArea } from "radix-ui";
import { useTranslation } from "react-i18next";
import {
	isLitmusRunActive,
	useLitmusProjectRepo,
	useLitmusRuns,
	useLitmusSettings,
	useRerunLitmusRun,
	useSetLitmusSettings,
	useStartLitmusRun,
	type LitmusRunSummary,
} from "../../hooks/useLitmus";
import { useWorkspaceQuery } from "../../hooks/useWorkspaceQuery";
import { hidesShellTopbar } from "../../lib/platform";
import { cn } from "../../lib/utils";
import { AppLink } from "../AppLink";
import { topbarProjectLabelClass } from "../TopbarButton";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Switch } from "../ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../ui/table";
import { formatDuration, formatStarted, litmusStatus, resolvePrInput } from "./litmus-format";
import { LitmusRunDrawer } from "./LitmusRunDrawer";
import { LitmusStatusBadge } from "./LitmusStatusBadge";

export function LitmusView({ projectId }: { projectId: string }) {
	const { t } = useTranslation();
	const showPageTitle = hidesShellTopbar();
	const projectName = useWorkspaceQuery().data?.find((workspace) => workspace.id === projectId)?.name ?? projectId;
	const runs = useLitmusRuns(projectId);
	const projectRepo = useLitmusProjectRepo(projectId);
	const settings = useLitmusSettings(projectId);
	const setSettings = useSetLitmusSettings(projectId);
	const start = useStartLitmusRun(projectId);
	const rerun = useRerunLitmusRun(projectId);
	const [prInput, setPrInput] = useState("");
	const [inputError, setInputError] = useState<string | null>(null);
	const [selectedRunId, setSelectedRunId] = useState<string | null>(null);
	const rows = runs.data ?? [];

	const submit = (event: FormEvent) => {
		event.preventDefault();
		const prUrl = resolvePrInput(prInput, projectRepo);
		if (!prUrl) {
			setInputError(t(prInput.trim() === "" ? "litmus.start.empty" : "litmus.start.needsUrl"));
			return;
		}
		setInputError(null);
		start.mutate(prUrl, { onSuccess: () => setPrInput("") });
	};
	const actionError = inputError ?? start.error?.message ?? rerun.error?.message ?? setSettings.error?.message ?? null;

	return (
		<div className="flex min-h-0 flex-1 flex-col bg-background" data-testid="litmus-view">
			<header className="workspace-topbar-container center-panel-titlebar flex h-toolbar shrink-0 items-center justify-between gap-3 border-b border-border-strong pr-2">
				<div className="flex min-w-0 items-center gap-2">
					<h1 className={showPageTitle ? cn(topbarProjectLabelClass, "inline-flex items-center gap-1.5") : "sr-only"}>
						{showPageTitle ? <FlaskConical className="size-icon-md" aria-hidden="true" /> : null}
						{t("litmus.title")}
					</h1>
					<p className="truncate text-xs text-muted-foreground">{t("litmus.description", { project: projectName })}</p>
				</div>
			</header>
			<ScrollArea.Root asChild type="hover" scrollHideDelay={500}>
				<main className="relative min-h-0 flex-1 overflow-hidden">
					<ScrollArea.Viewport className="h-full w-full overscroll-contain focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/60" tabIndex={0}>
						<div className="space-y-4 p-4">
							<div className="flex flex-wrap items-start justify-between gap-4">
								<form className="flex min-w-0 flex-1 basis-80 items-center gap-2" onSubmit={submit}>
									<Input
										aria-label={t("litmus.start.label")}
										placeholder={t("litmus.start.placeholder")}
										value={prInput}
										onChange={(event) => setPrInput(event.target.value)}
										className="max-w-md"
									/>
									<Button type="submit" disabled={start.isPending}>{t("litmus.start.run")}</Button>
								</form>
								<div className="flex items-center gap-2">
									<label htmlFor="litmus-ask-before-posting" className="text-sm">{t("litmus.askBeforePosting")}</label>
									<Switch
										id="litmus-ask-before-posting"
										checked={settings.data?.askBeforePosting ?? false}
										disabled={settings.isLoading || setSettings.isPending}
										onCheckedChange={(askBeforePosting) => setSettings.mutate({ askBeforePosting })}
									/>
								</div>
							</div>
							<p className="-mt-2 text-xs text-muted-foreground">{t("litmus.askBeforePostingHint")}</p>
							{actionError ? <p role="alert" className="rounded-md bg-destructive/10 px-3 py-2 text-xs text-destructive">{actionError}</p> : null}
							{runs.isLoading ? <p role="status" className="px-3 py-8 text-center text-sm text-muted-foreground">{t("litmus.loading")}</p> : null}
							{runs.error ? <p role="alert" className="text-sm text-destructive">{runs.error.message}</p> : null}
							{!runs.isLoading && !runs.error && rows.length === 0 ? (
								<div className="grid min-h-48 place-items-center rounded-lg border border-dashed border-border p-8 text-center">
									<div>
										<h2 className="font-medium">{t("litmus.empty.title")}</h2>
										<p className="mt-1 text-sm text-muted-foreground">{t("litmus.empty.description")}</p>
									</div>
								</div>
							) : null}
							{rows.length > 0 ? (
								<div className="overflow-hidden rounded-lg border border-border bg-background">
									<Table>
										<TableHeader>
											<TableRow className="text-xs text-muted-foreground">
												<TableHead>{t("litmus.col.pr")}</TableHead>
												<TableHead>{t("litmus.col.title")}</TableHead>
												<TableHead>{t("litmus.col.verdict")}</TableHead>
												<TableHead>{t("litmus.col.duration")}</TableHead>
												<TableHead>{t("litmus.col.started")}</TableHead>
												<TableHead>{t("litmus.col.comment")}</TableHead>
												<TableHead><span className="sr-only">{t("litmus.col.actions")}</span></TableHead>
											</TableRow>
										</TableHeader>
										<TableBody>
											{rows.map((run) => (
												<RunRow
													key={run.id}
													run={run}
													rerunPending={rerun.isPending && rerun.variables === run.id}
													onOpen={() => setSelectedRunId(run.id)}
													onRerun={() => rerun.mutate(run.id)}
												/>
											))}
										</TableBody>
									</Table>
								</div>
							) : null}
						</div>
					</ScrollArea.Viewport>
					<ScrollArea.Scrollbar orientation="vertical" className="z-10 flex w-2 touch-none select-none p-0.5">
						<ScrollArea.Thumb className="relative flex-1 rounded-full bg-[color-mix(in_srgb,var(--color-scrollbar)_42%,transparent)] hover:bg-[color-mix(in_srgb,var(--color-scrollbar)_56%,transparent)]" />
					</ScrollArea.Scrollbar>
				</main>
			</ScrollArea.Root>
			<LitmusRunDrawer projectId={projectId} runId={selectedRunId} onClose={() => setSelectedRunId(null)} />
		</div>
	);
}

function RunRow({
	run,
	rerunPending,
	onOpen,
	onRerun,
}: {
	run: LitmusRunSummary;
	rerunPending: boolean;
	onOpen: () => void;
	onRerun: () => void;
}) {
	const { t, i18n } = useTranslation();
	const commentState = run.commentState?.startsWith("failed") ? t("litmus.comment.failed") : run.commentState;
	return (
		<TableRow data-testid={`litmus-run-${run.id}`} data-run-id={run.id} className="text-sm">
			<TableCell>
				<AppLink href={run.pr.url} className="underline-offset-2 hover:underline">#{run.pr.number}</AppLink>
			</TableCell>
			<TableCell className="max-w-72">
				<button type="button" onClick={onOpen} className="block max-w-full truncate text-left underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60">
					{run.pr.title ?? run.repo}
				</button>
			</TableCell>
			<TableCell><LitmusStatusBadge status={litmusStatus(run)} /></TableCell>
			<TableCell className="tabular-nums">{formatDuration(run.durationSeconds)}</TableCell>
			<TableCell className="text-muted-foreground">{formatStarted(run.startedAt, i18n.language)}</TableCell>
			<TableCell>
				{run.commentUrl ? (
					<AppLink href={run.commentUrl} className="underline-offset-2 hover:underline">{t("litmus.comment.view")}</AppLink>
				) : (
					<span className="text-muted-foreground">{commentState ?? "-"}</span>
				)}
			</TableCell>
			<TableCell className="text-right">
				<Button size="sm" variant="outline" disabled={rerunPending || isLitmusRunActive(run)} onClick={onRerun} aria-label={t("litmus.rerunLabel", { number: run.pr.number })}>
					<RotateCw aria-hidden="true" />{t("litmus.rerun")}
				</Button>
			</TableCell>
		</TableRow>
	);
}

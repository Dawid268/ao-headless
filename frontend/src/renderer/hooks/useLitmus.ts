import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";

export type LitmusRunSummary = components["schemas"]["TestingRunSummary"];
export type LitmusRunDetail = components["schemas"]["TestingRunDetail"];
export type LitmusSettings = components["schemas"]["TestingProjectSettings"];
export type LitmusSettingsUpdate = components["schemas"]["UpdateTestingProjectSettingsRequest"];
export type LitmusCheck = components["schemas"]["TestingCheck"];

const RUNS_LIMIT = 50;
const ACTIVE_POLL_MS = 3_000;
const IDLE_POLL_MS = 15_000;

export const litmusRunsQueryKey = (projectId: string) => ["litmus", projectId, "runs"] as const;
export const litmusRunQueryKey = (runId: string) => ["litmus", "run", runId] as const;
export const litmusSettingsQueryKey = (projectId: string) => ["litmus", projectId, "settings"] as const;

/** A run the worker is still driving; the list polls faster while one exists. */
export function isLitmusRunActive(run: Pick<LitmusRunSummary, "state">): boolean {
	return run.state === "intake" || run.state === "prepared" || run.state === "running";
}

function litmusRunsQuery(projectId: string) {
	return {
		queryKey: litmusRunsQueryKey(projectId),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/testing/runs", {
				params: { query: { projectId, limit: RUNS_LIMIT } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return { runs: data?.runs ?? [], repo: data?.repo };
		},
		refetchInterval: (query: { state: { data?: { runs: LitmusRunSummary[] } } }) =>
			query.state.data?.runs.some(isLitmusRunActive) ? ACTIVE_POLL_MS : IDLE_POLL_MS,
	};
}

export function useLitmusRuns(projectId: string) {
	return useQuery({ ...litmusRunsQuery(projectId), select: (data) => data.runs });
}

/** The project's own GitHub repo (owner/repo), the one a bare PR number means; undefined when it has none. */
export function useLitmusProjectRepo(projectId: string) {
	return useQuery({ ...litmusRunsQuery(projectId), select: (data) => data.repo || undefined }).data;
}

export function useLitmusRun(runId: string | null) {
	return useQuery({
		queryKey: litmusRunQueryKey(runId ?? ""),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/testing/runs/{id}", {
				params: { path: { id: runId ?? "" } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			if (!data) throw new Error("Run response was empty");
			return data;
		},
		enabled: runId !== null,
		refetchInterval: (query) => (query.state.data && isLitmusRunActive(query.state.data) ? ACTIVE_POLL_MS : false),
	});
}

export function useLitmusSettings(projectId: string) {
	return useQuery({
		queryKey: litmusSettingsQueryKey(projectId),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/testing/projects/{id}/settings", {
				params: { path: { id: projectId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data ?? { askBeforePosting: false, review: false };
		},
	});
}

export function useSetLitmusSettings(projectId: string) {
	const client = useQueryClient();
	return useMutation({
		mutationFn: async (body: LitmusSettingsUpdate) => {
			const { data, error } = await apiClient.PUT("/api/v1/testing/projects/{id}/settings", {
				params: { path: { id: projectId } },
				body,
			});
			if (error) throw new Error(apiErrorMessage(error));
			return data ?? body;
		},
		onMutate: async (body) => {
			await client.cancelQueries({ queryKey: litmusSettingsQueryKey(projectId) });
			const previous = client.getQueryData<LitmusSettings>(litmusSettingsQueryKey(projectId));
			client.setQueryData<LitmusSettings>(litmusSettingsQueryKey(projectId), {
				...body,
				review: body.review ?? previous?.review ?? false,
			});
			return { previous };
		},
		onError: (_error, _body, context) => client.setQueryData(litmusSettingsQueryKey(projectId), context?.previous),
		// The server's value wins once the writes settle, however many toggles were in flight.
		onSettled: () => client.invalidateQueries({ queryKey: litmusSettingsQueryKey(projectId) }),
	});
}

function useRefreshRuns(projectId: string) {
	const client = useQueryClient();
	return (runId?: string) => {
		void client.invalidateQueries({ queryKey: litmusRunsQueryKey(projectId) });
		if (runId) void client.invalidateQueries({ queryKey: litmusRunQueryKey(runId) });
	};
}

export function useStartLitmusRun(projectId: string) {
	const refresh = useRefreshRuns(projectId);
	return useMutation({
		mutationFn: async (prUrl: string) => {
			const { data, error } = await apiClient.POST("/api/v1/testing/start", { body: { projectId, prUrl } });
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: () => refresh(),
	});
}

export function useRerunLitmusRun(projectId: string) {
	const refresh = useRefreshRuns(projectId);
	return useMutation({
		mutationFn: async (runId: string) => {
			const { data, error } = await apiClient.POST("/api/v1/testing/rerun", { body: { runId, projectId } });
			if (error) throw new Error(apiErrorMessage(error));
			return data;
		},
		onSuccess: () => refresh(),
	});
}

export function usePostLitmusRun(projectId: string) {
	const refresh = useRefreshRuns(projectId);
	return useMutation({
		mutationFn: async (runId: string) => {
			const { error } = await apiClient.POST("/api/v1/testing/runs/{id}/post", { params: { path: { id: runId } }, body: {} });
			if (error) throw new Error(apiErrorMessage(error));
			return runId;
		},
		onSuccess: (runId) => refresh(runId),
	});
}

export function useDiscardLitmusRun(projectId: string) {
	const refresh = useRefreshRuns(projectId);
	return useMutation({
		mutationFn: async (runId: string) => {
			const { error } = await apiClient.POST("/api/v1/testing/runs/{id}/discard", { params: { path: { id: runId } }, body: {} });
			if (error) throw new Error(apiErrorMessage(error));
			return runId;
		},
		onSuccess: (runId) => refresh(runId),
	});
}

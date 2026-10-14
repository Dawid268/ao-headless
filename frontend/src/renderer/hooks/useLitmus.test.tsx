import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { getMock, putMock } = vi.hoisted(() => ({ getMock: vi.fn(), putMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, PUT: putMock },
	apiErrorMessage: (error: unknown) => (error as { message?: string })?.message ?? "request failed",
}));

import { litmusSettingsQueryKey, useLitmusProjectRepo, useLitmusRuns, useLitmusSettings, useSetLitmusSettings } from "./useLitmus";

function wrapper(client: QueryClient) {
	return ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

const newClient = () => new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });

beforeEach(() => {
	getMock.mockReset();
	putMock.mockReset();
});

describe("useSetLitmusSettings", () => {
	it("keeps review enabled while toggling askBeforePosting without sending review", async () => {
		const client = newClient();
		let saved = { askBeforePosting: false, review: true };
		let finish!: () => void;
		getMock.mockImplementation(async () => ({ data: saved, error: undefined }));
		putMock.mockImplementation((_path: string, init: { body: { askBeforePosting: boolean } }) => new Promise((resolve) => {
			finish = () => {
				saved = { ...saved, ...init.body };
				resolve({ data: saved, error: undefined });
			};
		}));
		const { result } = renderHook(() => ({ settings: useLitmusSettings("p1"), set: useSetLitmusSettings("p1") }), { wrapper: wrapper(client) });
		await waitFor(() => expect(result.current.settings.data).toEqual(saved));
		await act(async () => { result.current.set.mutate({ askBeforePosting: true }); });
		await waitFor(() => expect(putMock).toHaveBeenCalledTimes(1));
		expect(putMock).toHaveBeenCalledWith("/api/v1/testing/projects/{id}/settings", {
			params: { path: { id: "p1" } }, body: { askBeforePosting: true },
		});
		expect(client.getQueryData(litmusSettingsQueryKey("p1"))).toEqual({ askBeforePosting: true, review: true });
		await act(async () => { finish(); });
		await waitFor(() => expect(result.current.set.isPending).toBe(false));
		await waitFor(() => expect(result.current.settings.data).toEqual({ askBeforePosting: true, review: true }));
	});

	it("ends on the server's value when two quick toggles both fail, not on the first optimistic one", async () => {
		const client = newClient();
		putMock.mockResolvedValue({ data: undefined, error: { message: "disk full" } });
		getMock.mockResolvedValue({ data: { askBeforePosting: false }, error: undefined }); // the server never changed

		const { result } = renderHook(() => ({ settings: useLitmusSettings("p1"), set: useSetLitmusSettings("p1") }), { wrapper: wrapper(client) });
		await waitFor(() => expect(result.current.settings.data).toEqual({ askBeforePosting: false }));
		await act(async () => {
			result.current.set.mutate({ askBeforePosting: true });
			result.current.set.mutate({ askBeforePosting: false });
		});
		await waitFor(() => expect(putMock).toHaveBeenCalledTimes(2));
		await waitFor(() => expect(result.current.set.isPending).toBe(false));
		await waitFor(() => expect(getMock.mock.calls.length).toBeGreaterThan(1)); // refetched after the writes settled
		await waitFor(() => expect(result.current.settings.data).toEqual({ askBeforePosting: false }));
	});

	it("keeps the saved value after a successful toggle", async () => {
		const client = newClient();
		let server = false;
		putMock.mockImplementation(async (_path: string, init: { body: { askBeforePosting: boolean } }) => {
			server = init.body.askBeforePosting;
			return { data: { askBeforePosting: server }, error: undefined };
		});
		getMock.mockImplementation(async () => ({ data: { askBeforePosting: server }, error: undefined }));

		const { result } = renderHook(() => ({ settings: useLitmusSettings("p1"), set: useSetLitmusSettings("p1") }), { wrapper: wrapper(client) });
		await waitFor(() => expect(result.current.settings.data).toEqual({ askBeforePosting: false }));
		await act(async () => {
			result.current.set.mutate({ askBeforePosting: true });
		});
		await waitFor(() => expect(result.current.settings.data).toEqual({ askBeforePosting: true }));
		expect(client.getQueryData(litmusSettingsQueryKey("p1"))).toEqual({ askBeforePosting: true });
	});
});

describe("useLitmusRuns and useLitmusProjectRepo", () => {
	it("share one request and read the runs and the project's own repo from it", async () => {
		getMock.mockResolvedValue({ data: { runs: [{ id: "r1", state: "submitted" }], repo: "acme/app" }, error: undefined });
		const client = newClient();
		const { result } = renderHook(() => ({ runs: useLitmusRuns("p1"), repo: useLitmusProjectRepo("p1") }), { wrapper: wrapper(client) });
		await waitFor(() => expect(result.current.repo).toBe("acme/app"));
		expect(result.current.runs.data).toEqual([{ id: "r1", state: "submitted" }]);
		expect(getMock).toHaveBeenCalledTimes(1);
	});

	it("reports no repo when the daemon names none", async () => {
		getMock.mockResolvedValue({ data: { runs: [{ id: "r1", repo: "other/project" }] }, error: undefined });
		const { result } = renderHook(() => ({ runs: useLitmusRuns("p1"), repo: useLitmusProjectRepo("p1") }), { wrapper: wrapper(newClient()) });
		await waitFor(() => expect(result.current.runs.data).toHaveLength(1));
		expect(result.current.repo).toBeUndefined();
	});
});

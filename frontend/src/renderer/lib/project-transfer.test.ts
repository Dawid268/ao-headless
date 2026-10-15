import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import {
	runProjectTransfer,
	type MergedTransferStatus,
} from "./project-transfer";

const post = vi.fn();
const get = vi.fn();
const del = vi.fn();

vi.mock("./api-client", () => ({
	apiClient: {
		POST: (...args: unknown[]) => post(...args),
		GET: (...args: unknown[]) => get(...args),
		DELETE: (...args: unknown[]) => del(...args),
	},
	apiErrorMessage: (_err: unknown, fallback: string) => fallback,
	getApiBaseUrl: () => "http://local.test",
}));

vi.mock("./host-clients", () => ({
	baseUrlForHost: () => "http://remote.test",
	clientForHost: () => ({
		POST: (...args: unknown[]) => post(...args),
		GET: (...args: unknown[]) => get(...args),
		DELETE: (...args: unknown[]) => del(...args),
	}),
}));

describe("runProjectTransfer", () => {
	beforeEach(() => {
		post.mockReset();
		get.mockReset();
		del.mockReset();
		vi.stubGlobal("fetch", vi.fn());
	});
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("merges export/upload/import progress and returns project id", async () => {
		post.mockResolvedValueOnce({ data: { jobId: "exp-1" }, error: undefined });
		get
			.mockResolvedValueOnce({
				data: {
					jobId: "exp-1",
					kind: "export",
					phase: "packing_source",
					percent: 50,
					done: false,
				},
				error: undefined,
			})
			.mockResolvedValueOnce({
				data: {
					jobId: "exp-1",
					kind: "export",
					phase: "done",
					percent: 100,
					done: true,
				},
				error: undefined,
			})
			.mockResolvedValueOnce({
				data: {
					jobId: "imp-1",
					kind: "import",
					phase: "importing",
					percent: 40,
					done: false,
				},
				error: undefined,
			})
			.mockResolvedValueOnce({
				data: {
					jobId: "imp-1",
					kind: "import",
					phase: "done",
					percent: 100,
					done: true,
					projectId: "dest-proj",
				},
				error: undefined,
			});

		const fetchMock = vi.mocked(fetch);
		fetchMock
			.mockResolvedValueOnce(
				new Response(new Uint8Array([1, 2, 3]).buffer, { status: 200 }),
			)
			.mockResolvedValueOnce(
				new Response(JSON.stringify({ jobId: "imp-1" }), {
					status: 202,
					headers: { "Content-Type": "application/json" },
				}),
			);

		const statuses: MergedTransferStatus[] = [];
		const result = await runProjectTransfer({
			projectId: "src",
			destDir: "/tmp/dest",
			includeSecrets: false,
			signal: new AbortController().signal,
			onStatus: (s) => statuses.push({ ...s }),
		});

		expect(result.phase).toBe("done");
		expect(result.projectId).toBe("dest-proj");
		expect(result.percent).toBe(100);
		expect(statuses.some((s) => s.phase === "exporting")).toBe(true);
		expect(statuses.some((s) => s.phase === "uploading")).toBe(true);
		expect(statuses.some((s) => s.phase === "importing")).toBe(true);
		expect(fetchMock).toHaveBeenCalled();
	});
});

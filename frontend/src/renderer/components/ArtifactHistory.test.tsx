import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { ArtifactVersionSummary } from "../hooks/useArtifactVersions";
import { ArtifactHistoryMenu, ArtifactVersionPreview, formatVersionTime } from "./ArtifactHistory";
import { TooltipProvider } from "./ui/tooltip";

const { versionsMock, versionMock, statusMock } = vi.hoisted(() => ({ versionsMock: vi.fn(), versionMock: vi.fn(), statusMock: vi.fn() }));
vi.mock("../lib/github-daemon", () => ({ getGitHubStatus: statusMock }));
vi.mock("../hooks/useArtifactVersions", () => ({
	useArtifactVersions: versionsMock,
	useArtifactVersion: versionMock,
}));

const at = (minute: number) => new Date(Date.UTC(2026, 9, 15, 12, minute)).toISOString();
const v = (version: number, source: "agent" | "user", minute: number, harness?: string): ArtifactVersionSummary => ({
	version,
	source,
	harness,
	size: 1,
	fileFingerprint: String(version),
	createdAt: at(minute),
});

function renderMenu(data: ArtifactVersionSummary[] | null, extra: { isError?: boolean } = {}) {
	statusMock.mockResolvedValue({ connected: true, login: "octocat" });
	versionsMock.mockReturnValue({
		data: data ?? undefined,
		isPending: data === null && !extra.isError,
		isError: Boolean(extra.isError),
		isSuccess: data !== null,
		refetch: vi.fn().mockResolvedValue({}),
	});
	const onSelect = vi.fn();
	render(
		<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
			<TooltipProvider>
				<ArtifactHistoryMenu fallbackHarness="codex" onSelect={onSelect} path="plan.md" selectedVersion={null} sessionId="s1" />
			</TooltipProvider>
		</QueryClientProvider>,
	);
	return { onSelect };
}

describe("ArtifactHistoryMenu", () => {
	it("lists every version in one list, the current one first, and previews a chosen version", async () => {
		const { onSelect } = renderMenu([v(4, "agent", 100, "claude-code"), v(3, "agent", 90, "claude-code"), v(2, "agent", 80), v(1, "user", 0)]);
		await userEvent.click(screen.getByRole("button", { name: "Version history" }));
		const menu = await screen.findByRole("menu");

		const items = within(menu).getAllByRole("menuitem");
		expect(items).toHaveLength(4);
		expect(within(items[0]!).getByText("Current")).toBeTruthy();
		// "Current" sits right after the time, on the left; the author mark is the last child.
		const parts = Array.from(items[0]!.children).map((child) => child.textContent);
		expect(parts[1]).toBe("Current");
		// The agent shows as its logo (named by the harness), a user save as a lettered avatar.
		expect(within(items[0]!).getByAltText(/claude/i)).toBeTruthy();
		// A version saved in the app wears the GitHub account's avatar, named by its username.
		expect(await within(items[3]!).findByTitle("octocat")).toBeTruthy();

		await userEvent.click(items[2]!);
		expect(onSelect).toHaveBeenCalledWith(2);
	});

	it("shows eight versions, then the rest after Show more", async () => {
		const many = Array.from({ length: 11 }, (_, i) => v(11 - i, i % 2 ? "user" : "agent", 1000 - i * 100, "claude-code"));
		renderMenu(many);
		await userEvent.click(screen.getByRole("button", { name: "Version history" }));
		const menu = await screen.findByRole("menu");
		expect(within(menu).getAllByRole("menuitem")).toHaveLength(9);
		await userEvent.click(within(menu).getByText("Show more"));
		expect(within(menu).getAllByRole("menuitem")).toHaveLength(11);
		expect(within(menu).queryByText("Show more")).toBeNull();
	});

	it("opens the live file from the current row", async () => {
		const { onSelect } = renderMenu([v(2, "agent", 10), v(1, "agent", 0)]);
		await userEvent.click(screen.getByRole("button", { name: "Version history" }));
		await userEvent.click(within(await screen.findByRole("menu")).getAllByRole("menuitem")[0]!);
		expect(onSelect).toHaveBeenCalledWith(null);
	});

	it("says so when there are no versions or loading failed", async () => {
		renderMenu([]);
		await userEvent.click(screen.getByRole("button", { name: "Version history" }));
		expect(await screen.findByText("No earlier versions yet.")).toBeTruthy();
	});

	it("reports a load error", async () => {
		renderMenu(null, { isError: true });
		await userEvent.click(screen.getByRole("button", { name: "Version history" }));
		expect(await screen.findByText("Unable to load version history")).toBeTruthy();
	});
});

describe("formatVersionTime", () => {
	it("names today and yesterday and dates anything older", () => {
		const now = new Date(2026, 9, 15, 20, 0);
		expect(formatVersionTime(new Date(2026, 9, 15, 8, 31).toISOString(), now)).toMatch(/^Today, /);
		expect(formatVersionTime(new Date(2026, 9, 14, 8, 31).toISOString(), now)).toMatch(/^Yesterday, /);
		// Day and month order follow the system locale.
		expect(formatVersionTime(new Date(2026, 9, 11, 8, 31).toISOString(), now)).toMatch(/^(Oct 11|11 Oct), /);
		expect(formatVersionTime("not a date", now)).toBe("");
	});
});

describe("ArtifactVersionPreview", () => {
	function renderPreview(props: Partial<React.ComponentProps<typeof ArtifactVersionPreview>> = {}) {
		versionMock.mockReturnValue({ data: { content: "the older text", createdAt: at(0) }, isPending: false, isError: false, dataUpdatedAt: 1 });
		const onRestore = vi.fn();
		const onBack = vi.fn();
		render(<ArtifactVersionPreview canRestore onBack={onBack} onRestore={onRestore} path="notes.txt" restoring={false} sessionId="s1" version={2} {...props} />);
		return { onBack, onRestore };
	}

	it("shows the version and offers Restore and Back to current", async () => {
		const { onBack, onRestore } = renderPreview();
		expect(screen.getByText("the older text")).toBeTruthy();
		await userEvent.click(screen.getByRole("button", { name: "Restore this version" }));
		expect(onRestore).toHaveBeenCalledTimes(1);
		await userEvent.click(screen.getByRole("button", { name: "Back to current" }));
		expect(onBack).toHaveBeenCalledTimes(1);
	});

	it("keeps Restore off until the live file's fingerprint is known, and while one is running", () => {
		renderPreview({ canRestore: false });
		expect((screen.getByRole("button", { name: "Restore this version" }) as HTMLButtonElement).disabled).toBe(true);
	});

	it("says Restoring while a restore runs, and shows a refused one", () => {
		renderPreview({ restoring: true, restoreError: "File changed while it was being edited" });
		expect((screen.getByRole("button", { name: "Restoring" }) as HTMLButtonElement).disabled).toBe(true);
		expect(screen.getByText("File changed while it was being edited")).toBeTruthy();
	});
});

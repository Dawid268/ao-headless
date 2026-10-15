import { render, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";

const get = vi.hoisted(() => vi.fn());

vi.mock("../../lib/api-client", () => ({
  apiClient: { GET: get, POST: vi.fn() },
  apiErrorMessage: () => "failed",
}));
vi.mock("../../hooks/useHostConnection", () => ({
  useConnectedHosts: () => [],
}));
vi.mock("../TerminalPane", () => ({ TerminalPane: () => null }));
vi.mock("../../lib/terminal-mux", () => ({
  createTerminalMux: vi.fn(),
  muxUrlFromApiBase: vi.fn(),
}));

import { SourceControlSettings } from "./SourceControlSettings";

it("offers install when a CLI is missing and sign-in when it is present", async () => {
  get.mockResolvedValue({
    data: {
      tools: [
        { id: "git", label: "Git", installed: true, detail: "/usr/bin/git" },
        {
          id: "gh",
          label: "GitHub CLI",
          installed: true,
          signedIn: false,
          detail: "/usr/bin/gh",
        },
        {
          id: "glab",
          label: "GitLab CLI",
          installed: false,
          detail: "glab was not found on PATH.",
        },
      ],
    },
    error: undefined,
  });
  render(<SourceControlSettings />);
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Sign in" })).toBeVisible(),
  );
  expect(screen.getByRole("button", { name: "Install" })).toBeVisible();
  expect(screen.getByText("Git")).toBeVisible();
  expect(get).toHaveBeenCalledWith("/api/v1/system/source-control");
});

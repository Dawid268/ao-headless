import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";

const clients = vi.hoisted(
  () =>
    new Map<
      string,
      { GET: ReturnType<typeof vi.fn>; POST: ReturnType<typeof vi.fn> }
    >(),
);
vi.mock("../lib/host-clients", () => ({
  clientForHost: (hostId: string) => clients.get(hostId),
}));
vi.mock("../lib/api-client", () => ({
  apiClient: { GET: vi.fn(), POST: vi.fn() },
  apiErrorMessage: (_err: unknown, fallback: string) => fallback,
}));

import { RemoteDirectoryPicker } from "./RemoteDirectoryPicker";

beforeEach(() => clients.clear());

it("browses the selected host's filesystem even when hosts contain the same path", async () => {
  const a = vi.fn(async () => ({
    data: {
      path: "/srv/todo-app",
      parent: "/srv",
      entries: [],
      truncated: false,
    },
  }));
  const b = vi.fn(async () => ({
    data: {
      path: "/srv/todo-app",
      parent: "/srv",
      entries: [],
      truncated: false,
    },
  }));
  clients.set("host-a", { GET: a, POST: vi.fn() });
  clients.set("host-b", { GET: b, POST: vi.fn() });
  const onChoose = vi.fn();
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const view = render(
    <QueryClientProvider client={client}>
      <RemoteDirectoryPicker
        hostId="host-a"
        hostLabel="Host A"
        connected
        initialPath="/srv/todo-app"
        onChoose={onChoose}
        onClose={vi.fn()}
      />
    </QueryClientProvider>,
  );
  await waitFor(() =>
    expect(a).toHaveBeenCalledWith("/api/v1/fs/dirs", {
      params: { query: { path: "/srv/todo-app" } },
    }),
  );
  await userEvent.click(
    await screen.findByRole("button", { name: "Use this folder" }),
  );
  expect(onChoose).toHaveBeenCalledWith("/srv/todo-app");
  view.unmount();
  render(
    <QueryClientProvider client={client}>
      <RemoteDirectoryPicker
        hostId="host-b"
        hostLabel="Host B"
        connected
        initialPath="/srv/todo-app"
        onChoose={onChoose}
        onClose={vi.fn()}
      />
    </QueryClientProvider>,
  );
  await waitFor(() =>
    expect(b).toHaveBeenCalledWith("/api/v1/fs/dirs", {
      params: { query: { path: "/srv/todo-app" } },
    }),
  );
  expect(a).toHaveBeenCalledTimes(1);
});

it("never calls a disconnected host or selects cached folders", async () => {
  const get = vi.fn();
  clients.set("host-a", { GET: get, POST: vi.fn() });
  const onChoose = vi.fn();
  render(
    <QueryClientProvider client={new QueryClient()}>
      <RemoteDirectoryPicker
        hostId="host-a"
        hostLabel="Host A"
        connected={false}
        onChoose={onChoose}
        onClose={vi.fn()}
      />
    </QueryClientProvider>,
  );
  expect(
    screen.getByRole("button", { name: "Use this folder" }),
  ).toBeDisabled();
  expect(get).not.toHaveBeenCalled();
  expect(onChoose).not.toHaveBeenCalled();
});

it("creates a nested folder on the host and navigates into it", async () => {
  const get = vi
    .fn()
    .mockResolvedValueOnce({
      data: {
        path: "/ao-data/projects",
        parent: "/ao-data",
        entries: [],
        truncated: false,
      },
    })
    .mockResolvedValue({
      data: {
        path: "/ao-data/projects/demo",
        parent: "/ao-data/projects",
        entries: [],
        truncated: false,
      },
    });
  const post = vi.fn(async () => ({
    data: {
      path: "/ao-data/projects/demo",
      parent: "/ao-data/projects",
      entries: [],
      truncated: false,
    },
  }));
  clients.set("host-a", { GET: get, POST: post });
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <RemoteDirectoryPicker
        hostId="host-a"
        hostLabel="VPS"
        connected
        initialPath="/ao-data/projects"
        onChoose={vi.fn()}
        onClose={vi.fn()}
      />
    </QueryClientProvider>,
  );
  await waitFor(() => expect(get).toHaveBeenCalled());
  await userEvent.click(
    await screen.findByRole("button", { name: "New folder" }),
  );
  await userEvent.type(screen.getByLabelText("Folder name"), "demo");
  await userEvent.click(screen.getByRole("button", { name: "Create" }));
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith("/api/v1/fs/dirs", {
      body: { path: "/ao-data/projects/demo" },
    }),
  );
});

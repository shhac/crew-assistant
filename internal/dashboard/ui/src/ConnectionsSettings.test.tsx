// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ConnectionsSettings, type Connection } from "./ConnectionsSettings";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("enables Notion with its native default account and no profile selection", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        tool: "agent-notion",
        profiles: [],
        available: true,
        selectable: false,
        detail: "Uses the CLI default account",
      }),
    }),
  );
  const changed = vi.fn();
  render(
    <ConnectionsSettings
      connections={[
        { id: "docs", name: "Documents", tool: "agent-notion", profiles: [] },
      ]}
      onChange={changed}
    />,
  );
  await waitFor(() =>
    expect(screen.getByText("Uses the CLI default account")).toBeTruthy(),
  );
  expect(
    screen.getByRole("option", { name: "Notion" }).hasAttribute("disabled"),
  ).toBe(false);
  expect(screen.getByText("Uses the account its CLI has")).toBeTruthy();
  expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
  expect(screen.queryByText(/None found/)).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Use the CLI's account" }),
  ).toBeNull();
  expect(changed).not.toHaveBeenCalled();
});
it("shows discovered Slack aliases and emits the selected profile", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        tool: "agent-slack",
        profiles: [
          { name: "work", detail: "Stored credential unavailable" },
          { name: "personal" },
        ],
        available: true,
        selectable: true,
        detail: "CLI accounts",
      }),
    }),
  );
  const changed = vi.fn();
  render(
    <ConnectionsSettings
      connections={[
        { id: "slack", name: "Slack", tool: "agent-slack", profiles: [] },
      ]}
      onChange={changed}
    />,
  );
  await waitFor(() => expect(screen.getAllByRole("checkbox")).toHaveLength(2));
  fireEvent.click(screen.getByRole("checkbox", { name: /personal/ }));
  expect(changed).toHaveBeenCalledWith([
    { id: "slack", name: "Slack", tool: "agent-slack", profiles: ["personal"] },
  ]);
  expect(screen.getByText("Stored credential unavailable")).toBeTruthy();
});

it.each([null, undefined])(
  "renders Notion with profiles %s from JSON config",
  async (profiles) => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({
          tool: "agent-notion",
          profiles: [],
          available: true,
          selectable: false,
          detail: "Native default account",
        }),
      }),
    );
    const connection = {
      id: "docs",
      name: "Documents",
      tool: "agent-notion",
      profiles,
    } as unknown as Connection;
    render(
      <ConnectionsSettings connections={[connection]} onChange={vi.fn()} />,
    );
    await waitFor(() =>
      expect(screen.getByText("Native default account")).toBeTruthy(),
    );
    expect(screen.getByText("Uses the account its CLI has")).toBeTruthy();
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
  },
);

function stubLinearProfiles() {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        tool: "lin",
        profiles: [{ name: "work" }],
        available: true,
        selectable: true,
        detail: "CLI accounts",
      }),
    }),
  );
}
it("keeps new Linear connections as resources until imports are explicitly enabled", async () => {
  stubLinearProfiles();
  const changed = vi.fn();
  const { rerender } = render(
    <ConnectionsSettings connections={[]} onChange={changed} />,
  );
  expect(screen.getByText("No connections.")).toBeTruthy();
  expect(
    screen.getByText(
      "Optional. They let the assistant read from services you use.",
    ),
  ).toBeTruthy();
  expect(
    screen.getByText(
      "Sign in with each service's own CLI; no passwords go in here.",
    ),
  ).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Add a connection" }));
  const added = changed.mock.calls.at(-1)![0] as Connection[];
  expect(added[0].tool).toBe("lin");
  expect(added[0].import_assignments).toBe(false);
  rerender(<ConnectionsSettings connections={added} onChange={changed} />);
  const group = screen.getByRole("group", { name: "New Linear connection" });
  expect(group.classList.contains("connection")).toBe(true);
  expect(group.querySelector("strong")?.textContent).toBe(
    "New Linear connection",
  );
  expect(group.textContent).toContain("Assignments and project context");
  expect(screen.getByText("Accounts it may use")).toBeTruthy();
  await screen.findByRole("checkbox", { name: "work" });
  const toggle = screen.getByRole<HTMLInputElement>("checkbox", {
    name: "Add issues assigned to you as projects",
  });
  expect(toggle.getAttribute("aria-describedby")).toBeTruthy();
  expect(
    document.getElementById(toggle.getAttribute("aria-describedby")!)
      ?.textContent,
  ).toBe("Leave off to use Linear only for reading.");
  expect(toggle.checked).toBe(false);
  fireEvent.click(toggle);
  const enabled = changed.mock.calls.at(-1)![0] as Connection[];
  expect(enabled[0].import_assignments).toBe(true);
  rerender(<ConnectionsSettings connections={enabled} onChange={changed} />);
  expect(toggle.checked).toBe(true);
  fireEvent.click(screen.getByRole("checkbox", { name: "work" }));
  const selected = changed.mock.calls.at(-1)![0] as Connection[];
  expect(selected[0]).toMatchObject({
    profiles: ["work"],
    import_assignments: true,
  });
  rerender(<ConnectionsSettings connections={selected} onChange={changed} />);
  fireEvent.click(toggle);
  expect(changed.mock.calls.at(-1)![0][0].import_assignments).toBe(false);
});
it("defaults saved connections without an import setting to context only", async () => {
  stubLinearProfiles();
  render(
    <ConnectionsSettings
      connections={[
        { id: "work", name: "Work", tool: "lin", profiles: ["work"] },
      ]}
      onChange={vi.fn()}
    />,
  );
  await screen.findByRole("checkbox", { name: "work" });
  expect(
    screen.getByRole<HTMLInputElement>("checkbox", {
      name: "Add issues assigned to you as projects",
    }).checked,
  ).toBe(false);
});
it("clears import permission and profiles when switching services", async () => {
  stubLinearProfiles();
  const changed = vi.fn();
  const { rerender } = render(
    <ConnectionsSettings
      connections={[
        {
          id: "work",
          name: "Work",
          tool: "lin",
          profiles: ["work"],
          import_assignments: true,
        },
      ]}
      onChange={changed}
    />,
  );
  await screen.findByRole("checkbox", { name: "work" });
  fireEvent.change(screen.getByRole("combobox", { name: "Service" }), {
    target: { value: "agent-notion" },
  });
  const updated = changed.mock.calls.at(-1)![0] as Connection[];
  expect(updated).toEqual([
    {
      id: "work",
      name: "Work",
      tool: "agent-notion",
      profiles: [],
      import_assignments: false,
    },
  ]);
  rerender(<ConnectionsSettings connections={updated} onChange={changed} />);
  expect(
    screen.queryByRole("checkbox", {
      name: "Add issues assigned to you as projects",
    }),
  ).toBeNull();
  await screen.findByText("Uses the account its CLI has");
});

it("keeps saved profiles that are not found now, without letting them be reselected", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        tool: "agent-slack",
        profiles: [{ name: "work" }],
        available: true,
        selectable: true,
        detail: "",
      }),
    }),
  );
  const changed = vi.fn();
  render(
    <ConnectionsSettings
      connections={[
        {
          id: "slack",
          name: "Team chat",
          tool: "agent-slack",
          profiles: ["old"],
        },
      ]}
      onChange={changed}
    />,
  );
  expect(screen.getByText("Workspaces it may use")).toBeTruthy();
  const saved = await screen.findByRole<HTMLInputElement>("checkbox", {
    name: /old/,
  });
  await waitFor(() =>
    expect(screen.getByText("Saved, but not found now")).toBeTruthy(),
  );
  expect(saved.checked).toBe(true);
  expect(saved.disabled).toBe(true);
  expect(
    screen.getByRole<HTMLInputElement>("checkbox", { name: "work" }).disabled,
  ).toBe(false);
  fireEvent.click(
    screen.getByRole("button", { name: "Remove connection Team chat" }),
  );
  expect(changed).toHaveBeenCalledWith([]);
});

it("offers to go back to the CLI's account when Notion has saved profiles", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        tool: "agent-notion",
        profiles: [],
        available: true,
        selectable: false,
        detail: "",
      }),
    }),
  );
  const changed = vi.fn();
  render(
    <ConnectionsSettings
      connections={[
        { id: "docs", name: "", tool: "agent-notion", profiles: ["legacy"] },
      ]}
      onChange={changed}
    />,
  );
  const group = screen.getByRole("group", { name: "New Notion connection" });
  expect(group.classList.contains("connection")).toBe(true);
  expect(group.querySelector("strong")?.textContent).toBe(
    "New Notion connection",
  );
  expect(screen.getByLabelText("Name")).toHaveProperty("placeholder", "Work");
  expect(screen.getByLabelText("Name")).toHaveProperty("maxLength", 80);
  expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
  fireEvent.click(
    await screen.findByRole("button", { name: "Use the CLI's account" }),
  );
  expect(changed).toHaveBeenCalledWith([
    { id: "docs", name: "", tool: "agent-notion", profiles: [] },
  ]);
  expect(
    screen.getByRole("button", { name: "Remove connection 1" }),
  ).toBeTruthy();
});

it("tells the owner how to sign in when a CLI has no accounts, and refreshes on request", async () => {
  const fetch = vi.fn().mockResolvedValue({
    ok: true,
    json: async () => ({
      tool: "agent-fathom",
      profiles: [],
      available: true,
      selectable: true,
      detail: "",
    }),
  });
  vi.stubGlobal("fetch", fetch);
  render(
    <ConnectionsSettings
      connections={[
        { id: "notes", name: "Meetings", tool: "agent-fathom", profiles: [] },
      ]}
      onChange={vi.fn()}
    />,
  );
  expect(screen.getByRole("button", { name: "Looking…" })).toHaveProperty(
    "disabled",
    true,
  );
  const hint = await screen.findByText(/None found\./);
  expect(hint.textContent).toBe(
    "None found. Sign in with the Fathom CLI (agent-fathom), then refresh.",
  );
  expect(hint.querySelector("code")?.textContent).toBe("agent-fathom");
  expect(fetch).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
  await screen.findByRole("button", { name: "Refresh" });
});

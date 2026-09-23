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
  expect(screen.getByText("CLI default account")).toBeTruthy();
  expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
  expect(screen.queryByText(/No profiles found/)).toBeNull();
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
    expect(screen.getByText("CLI default account")).toBeTruthy();
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
  expect(
    screen.getByText("Your projects live in crew-assistant."),
  ).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Add a connection" }));
  const added = changed.mock.calls.at(-1)![0] as Connection[];
  expect(added[0].tool).toBe("lin");
  expect(added[0].import_assignments).toBe(false);
  rerender(<ConnectionsSettings connections={added} onChange={changed} />);
  await screen.findByRole("checkbox", { name: "work" });
  const toggle = screen.getByRole("checkbox", {
    name: "Import assigned issues as projects",
  }) as HTMLInputElement;
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
    (
      screen.getByRole("checkbox", {
        name: "Import assigned issues as projects",
      }) as HTMLInputElement
    ).checked,
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
      name: "Import assigned issues as projects",
    }),
  ).toBeNull();
  await screen.findByText("CLI default account");
});

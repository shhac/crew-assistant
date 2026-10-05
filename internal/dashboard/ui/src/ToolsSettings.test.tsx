// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ToolsSettings } from "./ToolsSettings";
import { recordFetch, reply } from "./testFetch";
import type { Tool, Toolkit } from "./toolkit";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function tool(overrides: Partial<Tool> & { id: string }): Tool {
  const formula = `shhac/tap/${overrides.id}`;
  return {
    name: overrides.id,
    purpose: `Purpose of ${overrides.id}`,
    formula,
    status: "missing",
    install: `brew install ${formula}`,
    update: `brew upgrade ${formula}`,
    setup: [],
    verify: false,
    connection: false,
    ...overrides,
  };
}

const toolkit = (overrides: Partial<Toolkit> = {}): Toolkit => ({
  homebrew: { available: true, prefix: "/opt/homebrew" },
  npx: true,
  checked_at: new Date().toISOString(),
  tools: [
    tool({
      id: "lin",
      name: "Linear",
      status: "outdated",
      installed: "v0.36.4",
      latest: "v0.37.0",
      verify: true,
      verify_command: "lin user me",
      setup: ["lin auth login <api-key>"],
      skill: {
        name: "lin",
        status: "installed",
        installed: "2026-09-01",
        command: "npx skills add shhac/agent-skills --skill lin --global",
        runnable: true,
      },
    }),
    tool({
      id: "agent-mongo",
      name: "MongoDB",
      latest: "v0.14.0",
      skill: {
        name: "agent-mongo",
        status: "missing",
        command:
          "npx skills add shhac/agent-skills --skill agent-mongo --global",
        runnable: true,
        run: "npx --yes skills add shhac/agent-skills --skill agent-mongo --global --yes",
      },
    }),
    tool({
      id: "agent-sql",
      status: "outdated",
      installed: "v1.0.0",
      latest: "v1.1.0",
    }),
  ],
  ...overrides,
});

it("confirms the exact command, runs it, and follows its log", async () => {
  const polls: string[] = [];
  const { calls } = recordFetch((path, options) => {
    if (path === "/api/toolkit") return reply(toolkit());
    if (
      path === "/api/toolkit/agent-mongo/install" &&
      options?.method === "POST"
    )
      return reply(
        {
          id: "job1",
          tool: "agent-mongo",
          action: "install",
          command: "brew install shhac/tap/agent-mongo",
          state: "running",
          started_at: "2026-10-05T12:00:00Z",
          lines: ["==> Fetching"],
          next: 1,
        },
        202,
      );
    if (path.startsWith("/api/toolkit/jobs/job1")) {
      polls.push(path);
      return reply({
        id: "job1",
        tool: "agent-mongo",
        action: "install",
        command: "brew install shhac/tap/agent-mongo",
        state: "succeeded",
        result: "agent-mongo v0.14.0 is installed.",
        started_at: "2026-10-05T12:00:00Z",
        lines: ["==> Pouring agent-mongo"],
        next: 2,
      });
    }
    return reply({ error: "unexpected" }, 500);
  });
  render(<ToolsSettings pollMs={1} />);
  const row = (await screen.findByText("MongoDB")).closest("li")!;
  expect(within(row).getByText("Not installed")).toBeTruthy();
  fireEvent.click(within(row).getByRole("button", { name: "Install" }));
  const confirm = within(row).getByRole("group", { name: "Confirm command" });
  expect(
    within(confirm).getByText("brew install shhac/tap/agent-mongo"),
  ).toBeTruthy();
  expect(calls.some((c) => c.options?.method === "POST")).toBe(false);
  fireEvent.click(within(confirm).getByRole("button", { name: "Run" }));
  await screen.findByText("agent-mongo v0.14.0 is installed.");
  const log = screen.getByRole("region", { name: "Command output" });
  expect(log.textContent).toContain("==> Fetching\n==> Pouring agent-mongo");
  expect(polls[0]).toBe("/api/toolkit/jobs/job1?after=1");
  // The list is read again once the job ends.
  await waitFor(() =>
    expect(calls.filter((c) => c.path === "/api/toolkit")).toHaveLength(2),
  );
});

it("offers update all, skills and sign-in for what each tool needs", async () => {
  recordFetch((path) =>
    path === "/api/toolkit" ? reply(toolkit()) : reply({}, 500),
  );
  render(<ToolsSettings pollMs={1} />);
  const lin = (await screen.findByText("Linear")).closest("li")!;
  expect(within(lin).getByText("Update available")).toBeTruthy();
  expect(
    within(lin).getByText("v0.36.4 installed · v0.37.0 available"),
  ).toBeTruthy();
  expect(
    within(lin).queryByRole("button", { name: "Install skill" }),
  ).toBeNull();
  fireEvent.click(within(lin).getByRole("button", { name: "Check sign-in" }));
  expect(within(lin).getByText("lin user me")).toBeTruthy();
  expect(within(lin).getByText("lin auth login <api-key>")).toBeTruthy();
  const mongo = screen.getByText("MongoDB").closest("li")!;
  fireEvent.click(within(mongo).getByRole("button", { name: "Install skill" }));
  expect(
    within(mongo).getByText(
      "npx --yes skills add shhac/agent-skills --skill agent-mongo --global --yes",
    ),
  ).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Update all (2)" }));
  expect(
    screen.getByText("brew upgrade shhac/tap/lin shhac/tap/agent-sql"),
  ).toBeTruthy();
});

it("explains Homebrew and hands over the skill command when nothing can run here", async () => {
  recordFetch(() =>
    reply(
      toolkit({
        homebrew: {
          available: false,
          install: '/bin/bash -c "$(curl -fsSL https://example.invalid)"',
        },
        npx: false,
        tools: [
          tool({
            id: "agent-mongo",
            name: "MongoDB",
            skill: {
              name: "agent-mongo",
              status: "missing",
              command:
                "npx skills add shhac/agent-skills --skill agent-mongo --global",
              runnable: false,
            },
          }),
        ],
      }),
    ),
  );
  render(<ToolsSettings pollMs={1} />);
  await screen.findByText(/Homebrew isn't installed/);
  expect(
    screen.getByText('/bin/bash -c "$(curl -fsSL https://example.invalid)"'),
  ).toBeTruthy();
  expect(
    screen.getByRole<HTMLButtonElement>("button", { name: "Install" }).disabled,
  ).toBe(true);
  expect(screen.queryByRole("button", { name: "Install skill" })).toBeNull();
  expect(
    screen.getByText(
      "npx skills add shhac/agent-skills --skill agent-mongo --global",
    ),
  ).toBeTruthy();
});

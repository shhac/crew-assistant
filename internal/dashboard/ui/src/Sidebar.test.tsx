// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { Sidebar } from "./Sidebar";
import {
  normalizeState,
  type State,
  type UpgradeProgress,
  type Project,
} from "./api";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const sidebar = (stopping: boolean, paused = false, projects: Project[] = []) =>
  render(
    <Sidebar
      state={normalizeState({ stopping, paused, projects })}
      route={{ page: "inbox" }}
      needs={0}
      offline={false}
      chatOpen={false}
      onChat={() => {}}
      pausing={false}
      pauseError=""
      onPause={() => {}}
    />,
  );

describe("Sidebar status", () => {
  it("says the daemon is stopping and offers no pause meanwhile", () => {
    sidebar(true, true);
    expect(screen.getByText("Stopping")).toBeTruthy();
    expect(screen.getByText(/then crew-assistant stops/)).toBeTruthy();
    expect(screen.queryByText(/Steps already running finish/)).toBeNull();
    const pause = screen.getByRole("button", { name: "Resume teams" });
    expect((pause as HTMLButtonElement).disabled).toBe(true);
  });

  it("says running otherwise", () => {
    sidebar(false);
    expect(screen.getByText("Running")).toBeTruthy();
    expect(
      (
        screen.getByRole("button", {
          name: "Pause all teams",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false);
  });
});

const listed = (id: string, title: string, status: string): Project => ({
  id,
  title,
  status,
  brief: { version: 2, goal: "", criteria: [] },
});

describe("Sidebar projects", () => {
  it("lists projects alphabetically, whatever order they were made in", () => {
    sidebar(false, false, [
      listed("1", "lib-agent-harness", "active"),
      listed("2", "Demo Project", "active"),
      listed("3", "crew-code-review", "active"),
      listed("4", "Archived", "completed"),
    ]);
    const nav = screen
      .getByText("Projects", { selector: ".nav-label" })
      .closest(".nav-projects");
    const titles = Array.from(nav?.querySelectorAll("a.nav-link") ?? []).map(
      (a) => a.textContent?.trim(),
    );
    expect(titles).toEqual([
      "crew-code-review",
      "Demo Project",
      "lib-agent-harness",
    ]);
  });
});

it("shows engine resume and global resume independently in the sidebar", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({
      ok: true,
      json: async () => [{ engine: "claude", level: "ok", windows: [] }],
    })),
  );
  render(
    <Sidebar
      state={normalizeState({
        paused: true,
        engine_pauses: { claude: { at: new Date().toISOString() } },
      })}
      route={{ page: "inbox" }}
      needs={0}
      offline={false}
      chatOpen={false}
      onChat={() => {}}
      pausing={false}
      pauseError=""
      onPause={() => {}}
    />,
  );
  expect(
    await screen.findByRole("button", { name: "Resume Claude" }),
  ).toBeTruthy();
  expect(screen.getByRole("button", { name: "Resume teams" })).toBeTruthy();
  expect(screen.getByText(/Paused until you resume/)).toBeTruthy();
});

function progress(state: Partial<State>, offline = false) {
  return (
    <Sidebar
      state={normalizeState(state)}
      route={{ page: "inbox" }}
      needs={0}
      offline={offline}
      chatOpen={false}
      onChat={() => {}}
      pausing={false}
      pauseError=""
      onPause={() => {}}
    />
  );
}
const journal = (
  step: UpgradeProgress["step"],
  waiting_on: UpgradeProgress["waiting_on"] = null,
): UpgradeProgress => ({
  step,
  from: "1.0.0",
  to: "v1.1.0",
  since: "2026-10-04T10:00:00Z",
  waiting_on,
});

it.each([
  ["draining", "Finishing 0 steps"],
  ["backing-up", "Backing up"],
  ["installing", "Installing"],
  ["handing-over", "Starting v1.1.0"],
  ["probation", "Checking v1.1.0"],
] as const)("shows %s instead of stopping or paused", (step, hint) => {
  render(progress({ upgrade: journal(step), stopping: true, paused: true }));
  expect(screen.getByText("Upgrading to v1.1.0")).toBeTruthy();
  expect(screen.getByText(new RegExp(hint))).toBeTruthy();
  expect(screen.queryByText("Stopping")).toBeNull();
  expect(screen.queryByText("Paused")).toBeNull();
});

it.each([
  "healthy",
  "abandoned",
  "backup-failed",
  "install-failed",
  "rolled-back",
] as const)("does not present %s as an active installation", (step) => {
  const view = render(progress({ upgrade: journal(step) }));
  expect(screen.getByText("Running")).toBeTruthy();
  expect(screen.queryByText(/Upgrading to/)).toBeNull();
  view.rerender(progress({ upgrade: journal(step), stopping: true }));
  expect(screen.getByText("Stopping")).toBeTruthy();
});

it.each([
  ["rolling-back", "Restoring v1.0.0"],
  ["stopped-in-probation", "Upgrade interrupted"],
] as const)("describes %s without claiming success", (step, label) => {
  const view = render(progress({ upgrade: journal(step), stopping: true }));
  expect(screen.getByText(label)).toBeTruthy();
  view.rerender(progress({ upgrade: journal(step) }, true));
  expect(screen.getByText("Offline")).toBeTruthy();
  expect(screen.queryByText(label)).toBeNull();
});

it("keeps Offline authoritative during installation", () => {
  render(progress({ upgrade: journal("installing"), stopping: true }, true));
  expect(screen.getByText("Offline")).toBeTruthy();
  expect(screen.queryByText(/Upgrading to/)).toBeNull();
});

it("counts waiting work and updates durations and labels from each snapshot", () => {
  const now = vi
    .spyOn(Date, "now")
    .mockReturnValue(Date.parse("2026-10-04T12:00:00Z"));
  try {
    const waiting = [
      {
        kind: "implementer",
        ref: "task",
        label: "Build (Alex)",
        started_at: "2026-10-04T10:35:00Z",
      },
      {
        kind: "chat",
        ref: "reply",
        label: "assistant reply",
        started_at: "2026-10-04T11:58:00Z",
      },
    ];
    const view = render(progress({ upgrade: journal("draining", waiting) }));
    expect(screen.getByText(/Finishing 2 steps, longest 85m/)).toBeTruthy();
    expect(
      screen.getByText("Waiting on Build (Alex) (implementer) for 1h 25m"),
    ).toBeTruthy();
    expect(
      screen.getByText("Waiting on assistant reply (chat) for 2m"),
    ).toBeTruthy();
    now.mockReturnValue(Date.parse("2026-10-04T12:01:00Z"));
    view.rerender(progress({ upgrade: journal("draining", [waiting[0]]) }));
    expect(screen.getByText(/Finishing 1 step, longest 86m/)).toBeTruthy();
    expect(screen.queryByText(/Waiting on assistant/)).toBeNull();
    view.rerender(progress({ upgrade: journal("draining", null) }));
    expect(screen.getByText(/No running work remains/)).toBeTruthy();
    view.rerender(
      progress({
        upgrade: journal("draining", [
          { kind: "pm-chat", ref: "project", started_at: "bad" },
          { kind: "chat", ref: "", started_at: "2027-01-01T00:00:00Z" },
          { kind: "reviewer", ref: "zero", started_at: "0001-01-01T00:00:00Z" },
        ]),
      }),
    );
    expect(screen.getByText(/longest 0m/)).toBeTruthy();
    expect(
      screen.getByText("Waiting on project (pm-chat) for 0s"),
    ).toBeTruthy();
    expect(screen.getByText("Waiting on chat (chat) for 0s")).toBeTruthy();
    expect(screen.getByText("Waiting on zero (reviewer) for 0s")).toBeTruthy();
  } finally {
    now.mockRestore();
  }
});

it("keeps rollback protection during a retry and removes it only from new state", () => {
  const rollback = {
    from: "1.0.0",
    to: "1.1.0",
    failure: "<script>failed</script>",
    clear: "crew-assistant --state /custom/state upgrade clear-rollback",
  };
  const view = render(progress({ rollback, upgrade: journal("rolled-back") }));
  expect(
    screen.getByText(/Rollback is in force.*Restored v1.0.0/),
  ).toBeTruthy();
  expect(screen.getByText(/<script>failed/)).toBeTruthy();
  expect(view.container.querySelector("script")).toBeNull();
  expect(screen.getByText(rollback.clear)).toBeTruthy();
  expect(screen.getByText(/fresh backups/)).toBeTruthy();
  expect(screen.getByText(/custom state\/config flags/)).toBeTruthy();
  view.rerender(progress({ rollback, upgrade: journal("installing") }));
  expect(screen.getByText(/Rollback is in force/)).toBeTruthy();
  view.rerender(progress({ upgrade: journal("healthy") }));
  expect(screen.queryByText(/Rollback is in force/)).toBeNull();
});

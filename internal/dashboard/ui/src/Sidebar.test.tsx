// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { Sidebar } from "./Sidebar";
import { normalizeState, type Project } from "./api";

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

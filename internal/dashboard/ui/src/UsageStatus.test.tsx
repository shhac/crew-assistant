// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { UsageStatus, resetLabel } from "./UsageStatus";
import type { EngineUsage } from "./api";

let usage: EngineUsage[] | { status: number };
beforeEach(() => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => {
      const status = "status" in usage ? usage.status : 200;
      return {
        ok: status === 200,
        status,
        json: async () => ("status" in usage ? { error: "boom" } : usage),
      };
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const inAnHour = new Date(Date.now() + 60 * 60_000).toISOString();
const measured = (
  engine: string,
  left: number,
  level: EngineUsage["level"],
  extra: Partial<EngineUsage> = {},
): EngineUsage => ({
  engine,
  level,
  windows: [
    {
      name: "5-hour",
      left_percent: left,
      floor_percent: 10,
      resets_at: inAnHour,
      level,
    },
    { name: "weekly", left_percent: 80, floor_percent: 10, level: "ok" },
  ],
  ...extra,
});
const row = (name: string) =>
  screen.getByRole("listitem", { name: new RegExp(`^${name}:`) });

describe("usage left in the sidebar", () => {
  it("shows both engines, labelled, with what's left and when it resets", async () => {
    usage = [measured("codex", 42, "ok"), measured("claude", 70.4, "ok")];
    render(<UsageStatus />);
    await screen.findByRole("listitem", { name: /^Codex:/ });
    const codex = row("Codex");
    expect(codex.getAttribute("aria-label")).toBe(
      `Codex: 42% left, resets ${resetLabel(inAnHour)}`,
    );
    expect(within(codex).getByText("42% left")).toBeTruthy();
    expect(
      within(codex)
        .getByRole("meter", { name: "Codex usage left" })
        .getAttribute("aria-valuenow"),
    ).toBe("42");
    expect(row("Claude").getAttribute("aria-label")).toMatch(
      /^Claude: 70% left/,
    );
    expect(
      within(row("Claude")).getByText(/5-hour 70% · weekly 80%/),
    ).toBeTruthy();
    expect(codex.className).not.toMatch(/tone-/);
  });
  it("says plainly when a figure isn't there, and shows no number", async () => {
    usage = [
      {
        engine: "codex",
        level: "unknown",
        windows: [],
        missing: "not signed in",
      },
      {
        engine: "claude",
        level: "unknown",
        windows: [],
        missing: "usage not reported",
      },
    ];
    render(<UsageStatus />);
    expect(
      (await screen.findByRole("listitem", { name: "Codex: not signed in" }))
        .textContent,
    ).not.toMatch(/%/);
    expect(row("Claude").textContent).toContain("usage not reported");
    expect(screen.queryByRole("meter")).toBeNull();
  });
  it("highlights low and exhausted usage, and an engine rate-limited", async () => {
    const soon = new Date(Date.now() + 10 * 60_000).toISOString();
    usage = [
      measured("codex", 0, "exhausted", { resets_at: inAnHour }),
      measured("claude", 6, "low", { resets_at: inAnHour }),
    ];
    render(<UsageStatus />);
    const codex = await screen.findByRole("listitem", { name: /^Codex:/ });
    expect(codex.className).toContain("tone-block");
    expect(codex.getAttribute("aria-label")).toBe(
      `Codex: out of usage, resets ${resetLabel(inAnHour)}`,
    );
    expect(row("Claude").className).toContain("tone-needs");
    expect(row("Claude").getAttribute("aria-label")).toMatch(
      /^Claude: low, 6% left/,
    );
    cleanup();
    // Out when last measured, and nothing measured since: still out.
    usage = [
      {
        engine: "codex",
        level: "exhausted",
        windows: [],
        missing: "out of usage when last checked; usage check failed",
      },
      measured("claude", 50, "ok"),
    ];
    render(<UsageStatus />);
    const stillOut = await screen.findByRole("listitem", {
      name: "Codex: out of usage when last checked; usage check failed",
    });
    expect(stillOut.className).toContain("tone-block");
    expect(stillOut.textContent).not.toMatch(/%/);
    cleanup();
    // Rate-limited with no figure since: the small models skip it, so the
    // row says so too.
    usage = [
      {
        engine: "codex",
        level: "unknown",
        windows: [],
        missing: "usage check failed",
        rate_limited_until: soon,
      },
      measured("claude", 50, "ok"),
    ];
    render(<UsageStatus />);
    const limitedUnmeasured = await screen.findByRole("listitem", {
      name: `Codex: rate-limited until ${resetLabel(soon)}, usage check failed`,
    });
    expect(limitedUnmeasured.className).toContain("tone-block");
    expect(limitedUnmeasured.textContent).toContain("rate-limited until");
    expect(limitedUnmeasured.textContent).not.toMatch(/%/);
    cleanup();
    usage = [
      measured("codex", 50, "ok", { rate_limited_until: soon }),
      measured("claude", 50, "ok"),
    ];
    render(<UsageStatus />);
    const limited = await screen.findByRole("listitem", { name: /^Codex:/ });
    expect(limited.className).toContain("tone-block");
    expect(limited.getAttribute("aria-label")).toBe(
      `Codex: rate-limited until ${resetLabel(soon)}, 50% left, resets ${resetLabel(inAnHour)}`,
    );
  });
  it("says when usage couldn't be checked, instead of any figure", async () => {
    usage = { status: 500 };
    render(<UsageStatus />);
    expect(await screen.findByText("Usage couldn't be checked.")).toBeTruthy();
    expect(screen.queryByRole("listitem")).toBeNull();
  });
});

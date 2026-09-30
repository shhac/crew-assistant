// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import {
  UsageStatus,
  resetLabel,
  asOfLabel,
  usageRefreshMs,
} from "./UsageStatus";
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
  it("names an API provider resting for its rate limit, and until when", async () => {
    usage = [
      measured("codex", 42, "ok"),
      {
        engine: "openai-compatible",
        provider: "openrouter",
        label: "OpenRouter",
        level: "unknown",
        windows: [],
        rate_limited_until: inAnHour,
      },
    ];
    render(<UsageStatus />);
    const limited = await screen.findByRole("listitem", {
      name: /^OpenRouter:/,
    });
    expect(limited.getAttribute("aria-label")).toBe(
      `OpenRouter: rate-limited until ${resetLabel(inAnHour)}`,
    );
    expect(
      within(limited).getByText(`Rate-limited until ${resetLabel(inAnHour)}`),
    ).toBeTruthy();
    expect(limited.className).toMatch(/tone-block/);
    expect(within(limited).queryByRole("meter")).toBeNull();
    expect(row("Codex")).toBeTruthy();
  });
  it("shows the credit an engine reports beside its usage", async () => {
    usage = [
      measured("codex", 42, "ok", {
        credits: { balance: "12.50", unit: "USD" },
      }),
      {
        engine: "grok",
        level: "unknown",
        windows: [],
        missing: "usage not reported",
        credits: { balance: "12.5", unit: "credits" },
      },
    ];
    render(<UsageStatus />);
    await screen.findByRole("listitem", { name: /^Codex:/ });
    expect(within(row("Codex")).getByText(/Credits: 12\.50 USD/)).toBeTruthy();
    expect(row("Codex").getAttribute("aria-label")).toMatch(
      /Credits: 12\.50 USD$/,
    );
    expect(within(row("Grok")).getByText("12.5 credits")).toBeTruthy();
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

it("labels retained figures and their reason, including credits alone", async () => {
  const at = new Date(Date.now() - 25 * 60 * 60_000).toISOString();
  usage = [
    measured("codex", 42, "ok", { as_of: at, missing: "usage check failed" }),
    {
      engine: "claude",
      level: "unknown",
      windows: [],
      credits: { balance: "12", unit: "credits" },
      as_of: at,
      missing: "usage check timed out",
    },
  ];
  render(<UsageStatus />);
  await screen.findByRole("listitem", { name: /^Codex:/ });
  expect(
    within(row("Codex")).getByText(`42% left · ${asOfLabel(at)}`),
  ).toBeTruthy();
  expect(row("Codex").getAttribute("aria-label")).toContain(asOfLabel(at));
  expect(within(row("Codex")).getByText(/usage check failed/)).toBeTruthy();
  expect(
    within(row("Claude")).getByText(`12 credits · ${asOfLabel(at)}`),
  ).toBeTruthy();
  expect(asOfLabel(at)).toContain(
    new Date(at).toLocaleString(undefined, { weekday: "short" }),
  );
});

it("dates held rows after a failed fetch and clears the date on a fresh response", async () => {
  vi.useFakeTimers();
  try {
    const at = new Date();
    vi.setSystemTime(at);
    usage = [measured("codex", 42, "ok")];
    render(<UsageStatus />);
    await act(async () => {
      await Promise.resolve();
    });
    expect(row("Codex").getAttribute("aria-label")).not.toContain("as of");
    usage = { status: 500 };
    await act(async () => {
      await vi.advanceTimersByTimeAsync(usageRefreshMs);
    });
    expect(
      within(row("Codex")).getByText(
        `42% left · ${asOfLabel(at.toISOString())}`,
      ),
    ).toBeTruthy();
    expect(row("Codex").getAttribute("aria-label")).toContain(
      asOfLabel(at.toISOString()),
    );
    usage = [measured("codex", 70, "ok")];
    await act(async () => {
      await vi.advanceTimersByTimeAsync(usageRefreshMs);
    });
    expect(within(row("Codex")).getByText("70% left")).toBeTruthy();
    expect(row("Codex").getAttribute("aria-label")).not.toContain("as of");
  } finally {
    vi.useRealTimers();
  }
});

it.each([undefined, new Date(Date.now() - 60_000).toISOString()])(
  "keeps credits-only exhaustion visible with as_of %s",
  async (as_of) => {
    const missing = "out of usage when last checked; usage not reported";
    usage = [
      {
        engine: "codex",
        level: "exhausted",
        windows: [],
        credits: { balance: "12", unit: "credits" },
        missing,
        as_of,
      },
    ];
    render(<UsageStatus />);
    const codex = await screen.findByRole("listitem", { name: /^Codex:/ });
    expect(codex.className).toContain("tone-block");
    expect(
      within(codex).getByText(
        ["Out of usage", asOfLabel(as_of)].filter(Boolean).join(" · "),
      ),
    ).toBeTruthy();
    expect(codex.getAttribute("aria-label")).toContain("out of usage");
    expect(codex.getAttribute("aria-label")).toContain(missing);
    expect(within(codex).getByText(/12 credits/)).toBeTruthy();
    expect(within(codex).getByText(new RegExp(missing))).toBeTruthy();
  },
);

it("shows the reason alongside fresh credits even without windows", async () => {
  usage = [
    {
      engine: "codex",
      level: "unknown",
      windows: [],
      credits: { balance: "12", unit: "credits" },
      missing: "usage not reported",
    },
  ];
  render(<UsageStatus />);
  const codex = await screen.findByRole("listitem", { name: /^Codex:/ });
  expect(within(codex).getByText("12 credits")).toBeTruthy();
  expect(within(codex).getByText("usage not reported")).toBeTruthy();
});

it("dates held figures, but not unavailable rows or API providers", async () => {
  vi.useFakeTimers();
  try {
    usage = [
      {
        engine: "codex",
        level: "unknown",
        windows: [],
        missing: "not installed",
      },
      {
        engine: "claude",
        level: "unknown",
        windows: [],
        missing: "usage check failed",
      },
      {
        engine: "grok",
        level: "unknown",
        windows: [],
        credits: { balance: "0", unit: "credits" },
      },
      {
        engine: "openai-compatible",
        provider: "fixture",
        label: "Fixture",
        level: "unknown",
        windows: [],
        rate_limited_until: inAnHour,
      },
    ];
    render(<UsageStatus />);
    await act(async () => {
      await Promise.resolve();
    });
    usage = { status: 500 };
    await act(async () => {
      await vi.advanceTimersByTimeAsync(usageRefreshMs);
    });
    for (const name of ["Codex", "Claude", "Fixture"]) {
      expect(row(name).textContent).not.toContain("as of");
      expect(row(name).getAttribute("aria-label")).not.toContain("as of");
    }
    expect(row("Grok").textContent).toContain("as of");
  } finally {
    vi.useRealTimers();
  }
});

it.each(["ok", "low", "exhausted"] as const)(
  "omits past resets for retained %s windows but keeps future resets",
  async (level) => {
    const past = new Date(Date.now() - 60_000).toISOString();
    const as_of = new Date(Date.now() - 120_000).toISOString();
    usage = [
      measured("codex", 42, level, {
        as_of,
        resets_at: past,
        windows: [
          {
            name: "5-hour",
            left_percent: 42,
            floor_percent: 10,
            level,
            resets_at: past,
          },
        ],
      }),
      measured("claude", 42, level, { as_of, resets_at: inAnHour }),
    ];
    render(<UsageStatus />);
    await screen.findByRole("listitem", { name: /^Codex:/ });
    expect(row("Codex").textContent).not.toContain("Resets");
    expect(row("Codex").getAttribute("aria-label")).not.toContain("resets");
    expect(row("Codex").textContent).toContain("as of");
    expect(row("Claude").textContent).toContain("Resets");
  },
);

it("omits a past reset when retaining a row after a fetch failure", async () => {
  vi.useFakeTimers();
  try {
    const soon = new Date(Date.now() + 60_000).toISOString();
    usage = [
      measured("codex", 42, "ok", {
        windows: [
          {
            name: "5-hour",
            left_percent: 42,
            floor_percent: 10,
            level: "ok",
            resets_at: soon,
          },
        ],
      }),
    ];
    render(<UsageStatus />);
    await act(async () => {
      await Promise.resolve();
    });
    expect(row("Codex").textContent).toContain("Resets");
    usage = { status: 500 };
    await act(async () => {
      await vi.advanceTimersByTimeAsync(usageRefreshMs);
    });
    expect(row("Codex").textContent).not.toContain("Resets");
    expect(row("Codex").textContent).toContain("as of");
  } finally {
    vi.useRealTimers();
  }
});

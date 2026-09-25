// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { LimitsSettings } from "./LimitsSettings";
import type { Config } from "./api";

afterEach(cleanup);

const engines = {
  codex: {
    bin: "/test/codex",
    home: "/test/codex-home",
    usage_floor: { "5h_percent": 25 },
  },
  claude: { home: "/test/claude-home", on_unknown_usage: "pause" },
};
const config = {
  limits: { max_model_calls_per_day: 200, max_model_turns: 8 },
  engines,
} as Config;
const defaults = { usage_floor: 10, on_unknown_usage: "allow" as const };

function panel(name: string) {
  return within(screen.getByRole("region", { name }));
}

it("shows each engine's floors, with the default in place of a blank", () => {
  render(
    <LimitsSettings config={config} defaults={defaults} onChange={() => {}} />,
  );
  const codex = panel("Codex subscription");
  const fiveHour = codex.getByLabelText<HTMLInputElement>(
    "Keep unused of the 5-hour window (%)",
  );
  const weekly = codex.getByLabelText<HTMLInputElement>(
    "Keep unused of the weekly window (%)",
  );
  expect(fiveHour.value).toBe("25");
  expect(weekly.value).toBe("");
  expect(weekly.placeholder).toBe("10");
  expect(
    codex.getByLabelText<HTMLSelectElement>("If use can't be read").value,
  ).toBe("allow");
  expect(
    panel("Claude subscription").getByLabelText<HTMLSelectElement>(
      "If use can't be read",
    ).value,
  ).toBe("pause");
});

it("sends 0 as 0, and drops a blanked floor so the default applies", () => {
  const changed = vi.fn();
  render(
    <LimitsSettings config={config} defaults={defaults} onChange={changed} />,
  );
  const codex = panel("Codex subscription");
  fireEvent.change(
    codex.getByLabelText("Keep unused of the weekly window (%)"),
    { target: { value: "0" } },
  );
  expect(changed).toHaveBeenLastCalledWith({
    ...config,
    engines: {
      ...engines,
      codex: {
        ...engines.codex,
        usage_floor: { "5h_percent": 25, "1w_percent": 0 },
      },
    },
  });
  fireEvent.change(
    codex.getByLabelText("Keep unused of the 5-hour window (%)"),
    { target: { value: "" } },
  );
  const cleared = changed.mock.lastCall![0];
  expect(cleared.engines.codex).toEqual({
    bin: "/test/codex",
    home: "/test/codex-home",
  });
  expect(JSON.stringify(cleared.engines.codex)).not.toContain("usage_floor");
  expect(cleared.engines.claude).toBe(engines.claude);
});

it("sets one engine's unknown-usage choice without touching the other's settings", () => {
  const changed = vi.fn();
  render(<LimitsSettings config={config} onChange={changed} />);
  fireEvent.change(
    panel("Codex subscription").getByLabelText("If use can't be read"),
    { target: { value: "pause" } },
  );
  expect(changed).toHaveBeenLastCalledWith({
    ...config,
    engines: {
      ...engines,
      codex: { ...engines.codex, on_unknown_usage: "pause" },
    },
  });
  fireEvent.change(
    panel("Claude subscription").getByLabelText(
      "Keep unused of the 5-hour window (%)",
    ),
    { target: { value: "15" } },
  );
  expect(changed).toHaveBeenLastCalledWith({
    ...config,
    engines: {
      ...engines,
      claude: { ...engines.claude, usage_floor: { "5h_percent": 15 } },
    },
  });
});

it("keeps the model call limits where they are", () => {
  const changed = vi.fn();
  render(<LimitsSettings config={config} onChange={changed} />);
  fireEvent.change(screen.getByLabelText("Per day"), {
    target: { value: "300" },
  });
  expect(changed).toHaveBeenLastCalledWith({
    ...config,
    limits: { max_model_calls_per_day: 300, max_model_turns: 8 },
  });
});

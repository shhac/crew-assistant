import { expect, it } from "vitest";
import {
  choicesFor,
  engineLabel,
  engineOptions,
  unavailableForRoles,
} from "./engines";
import { testChoices } from "./testEngines";

it("labels an engine from its choice, or its id before they load", () => {
  expect(engineLabel("openai-compatible", testChoices)).toBe("Another API");
  expect(engineLabel("nova", testChoices)).toBe("Nova");
});

it("filters engines by what they may be used for", () => {
  const engines = (use: Parameters<typeof choicesFor>[1]) =>
    choicesFor(testChoices, use).map((choice) => choice.engine);
  expect(engines("roles")).toEqual(["codex", "claude"]);
  expect(engines("assistant")).toEqual([
    "codex",
    "claude",
    "openai-compatible",
  ]);
  expect(engines("cli")).toEqual(["codex", "claude", "grok"]);
  expect(engines("usage")).toEqual(["codex", "claude"]);
  expect(engines("compact")).toEqual(["codex"]);
});

it("lists the engines roles can't run on yet, with the server's reason", () => {
  expect(unavailableForRoles(testChoices)).toEqual([]);
  const reason =
    "Another API can't run team roles on this computer: commands require a sandbox the harness can prove only on macOS.";
  expect(
    unavailableForRoles(
      testChoices.map((choice) =>
        choice.engine === "openai-compatible"
          ? { ...choice, roles_reason: reason }
          : choice,
      ),
    ),
  ).toEqual([{ id: "openai-compatible", label: "Another API", reason }]);
});

it("keeps a chosen engine that isn't offered for the use", () => {
  expect(engineOptions(testChoices, "roles", "grok")).toEqual([
    { id: "codex", label: "Codex" },
    { id: "claude", label: "Claude" },
    { id: "grok", label: "Grok" },
  ]);
  expect(engineOptions(testChoices, "roles", "claude")).toHaveLength(2);
});

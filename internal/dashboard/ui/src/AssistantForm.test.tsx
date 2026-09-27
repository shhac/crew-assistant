// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { AssistantForm } from "./AssistantForm";
import { rememberChoices } from "./engines";
import { testChoices } from "./testEngines";

rememberChoices(testChoices);

beforeEach(() => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ available: false, engine: "", models: [] }),
    })),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("offers only engines that may run the assistant, starting on the first", () => {
  render(<AssistantForm onSaved={async () => {}} onCancel={() => {}} />);
  const engine = screen.getByLabelText<HTMLSelectElement>("Engine");
  expect(Array.from(engine.options, (o) => o.textContent)).toEqual([
    "Codex",
    "Claude",
    "Another API",
  ]);
  expect(engine.value).toBe("codex");
  expect(screen.queryByLabelText(/^Most output tokens/)).toBeNull();
});

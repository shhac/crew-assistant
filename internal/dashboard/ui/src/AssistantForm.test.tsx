// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
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

it("lets the assistant be allowed browser use, and says what that opens", async () => {
  const onSaved = vi.fn(async () => {});
  render(<AssistantForm onSaved={onSaved} onCancel={() => {}} />);
  fireEvent.change(screen.getByLabelText("Name"), {
    target: { value: "Iris" },
  });
  fireEvent.change(screen.getByLabelText("Engine"), {
    target: { value: "claude" },
  });
  fireEvent.click(screen.getByRole("checkbox", { name: "Allow browser use" }));
  expect(
    screen.getByText("This is your real Chrome, with its logins."),
  ).toBeTruthy();
  expect(screen.getByText(/read files on this machine/)).toBeTruthy();
  // An engine that can't use a browser can't be saved with it on.
  fireEvent.change(screen.getByLabelText("Engine"), {
    target: { value: "codex" },
  });
  expect(screen.getByText(/Codex can't use the browser/)).toBeTruthy();
  const add = screen.getByRole("button", { name: "Add assistant" });
  expect(add.hasAttribute("disabled")).toBe(true);
  fireEvent.change(screen.getByLabelText("Engine"), {
    target: { value: "claude" },
  });
  fireEvent.change(screen.getByLabelText(/^Connected browser/), {
    target: { value: " Work " },
  });
  fireEvent.click(add);
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
  const calls = vi.mocked(fetch).mock.calls;
  const [, init] = calls[calls.length - 1];
  expect(JSON.parse(String(init?.body))).toMatchObject({
    name: "Iris",
    model: { engine: "claude" },
    browser: { on: true, name: "Work" },
  });
});

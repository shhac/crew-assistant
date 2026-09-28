// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { MemberForm } from "./MemberForm";
import type { Member } from "./api";
import { rememberChoices } from "./engines";
import { testChoices } from "./testEngines";

rememberChoices(testChoices);

const listed = (): Record<string, unknown> => ({
  claude: {
    available: true,
    engine: "claude",
    detail: "Reported by Claude",
    default: { model: "sonnet", effort: "" },
    models: [
      { id: "sonnet", name: "Test Sonnet", default_effort: "", efforts: [] },
      {
        id: "opus",
        name: "Test Opus",
        default_effort: "",
        efforts: [],
        is_default: true,
      },
    ],
  },
  codex: {
    available: true,
    engine: "codex",
    detail: "Reported by Codex",
    default: { model: "thinker", effort: "high" },
    models: [
      {
        id: "thinker",
        name: "Test Thinker",
        default_effort: "high",
        efforts: [],
      },
      {
        id: "builder",
        name: "Test Builder",
        default_effort: "low",
        efforts: [],
      },
    ],
  },
});
let catalogs: Record<string, unknown>;
let calls: { path: string; options?: RequestInit }[];
beforeEach(() => {
  catalogs = listed();
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, options?: RequestInit) => {
      calls.push({ path, options });
      const engine = new URL(path, "http://test").searchParams.get("engine");
      const body = path.startsWith("/api/models")
        ? catalogs[engine ?? ""]
        : { id: "m1", ...JSON.parse(String(options?.body)) };
      return { ok: true, status: 200, json: async () => body };
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const choices = () =>
  Array.from(
    (screen.getByLabelText(/^Model/) as HTMLSelectElement).options,
    (o) => o.textContent,
  );
const saved = () => {
  const save = calls.find((c) => c.path.startsWith("/api/members"))!;
  return {
    method: save.options?.method,
    body: JSON.parse(String(save.options?.body)),
  };
};

it("chooses a new member's model from the models its engine offers", async () => {
  const onSaved = vi.fn();
  render(<MemberForm onSaved={onSaved} onCancel={() => {}} />);
  await screen.findByRole("option", { name: "Test Sonnet (recommended)" });
  expect(screen.getByLabelText(/^Model/).tagName).toBe("SELECT");
  expect(choices()).toEqual([
    "The engine's default",
    "Test Sonnet (recommended)",
    "Test Opus (Claude default)",
  ]);
  fireEvent.change(screen.getByLabelText(/^Model/), {
    target: { value: "opus" },
  });
  fireEvent.change(screen.getByLabelText("Engine"), {
    target: { value: "codex" },
  });
  await screen.findByRole("option", { name: "Test Thinker (recommended)" });
  expect(choices()).toEqual([
    "The engine's default",
    "Test Thinker (recommended)",
    "Test Builder",
  ]);
  expect(screen.getByLabelText(/^Model/)).toHaveProperty("value", "");
  expect(
    calls.filter((c) => c.path.startsWith("/api/models")).map((c) => c.path),
  ).toEqual([
    "/api/models?profile=assistant&engine=claude",
    "/api/models?profile=assistant&engine=codex",
  ]);
  fireEvent.change(screen.getByLabelText(/^Model/), {
    target: { value: "builder" },
  });
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Ada" } });
  fireEvent.change(screen.getByLabelText(/^Description/), {
    target: { value: " Tall, silver-haired, fond of tidy diffs. " },
  });
  fireEvent.change(screen.getByLabelText(/^Personality/), {
    target: { value: " Dry wit, short sentences. " },
  });
  fireEvent.click(screen.getByRole("button", { name: "Add member" }));
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
  expect(saved()).toEqual({
    method: "POST",
    body: {
      name: "Ada",
      kinds: ["implementer"],
      engine: "codex",
      model: "builder",
      effort: "",
      instructions: "",
      description: "Tall, silver-haired, fond of tidy diffs.",
      personality: "Dry wit, short sentences.",
      browser: { on: false, name: "" },
    },
  });
});

it("lets QA on an engine with a browser use it, and says whose browser it is", async () => {
  const onSaved = vi.fn();
  render(<MemberForm onSaved={onSaved} onCancel={() => {}} />);
  await screen.findByRole("option", { name: "Test Sonnet (recommended)" });
  // Only QA uses the browser.
  expect(screen.queryByText("QA uses the browser to try the app")).toBeNull();
  fireEvent.click(screen.getByRole("checkbox", { name: "Implementer" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "QA" }));
  fireEvent.click(
    screen.getByRole("checkbox", {
      name: "QA uses the browser to try the app",
    }),
  );
  expect(
    screen.getByText("This is your real Chrome, with its logins."),
  ).toBeTruthy();
  fireEvent.change(screen.getByLabelText(/^Connected browser/), {
    target: { value: " Work laptop " },
  });
  fireEvent.change(screen.getByLabelText("Name"), {
    target: { value: "Quinn" },
  });
  // Codex has no browser QA can use, so it can't be saved with it on.
  fireEvent.change(screen.getByLabelText("Engine"), {
    target: { value: "codex" },
  });
  expect(screen.getByText(/QA on Codex can't use the browser/)).toBeTruthy();
  expect(
    screen.getByRole("button", { name: "Add member" }).hasAttribute("disabled"),
  ).toBe(true);
  fireEvent.change(screen.getByLabelText("Engine"), {
    target: { value: "claude" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Add member" }));
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
  expect(saved().body).toMatchObject({
    kinds: ["qa"],
    engine: "claude",
    browser: { on: true, name: "Work laptop" },
  });
});

it("fills a new member in from a suggestion, and has its look drawn", async () => {
  const onSaved = vi.fn();
  const suggestion = {
    id: "s1",
    name: "Moss",
    personality: "Careful and kind.",
    rationale: "Fits a reviewer.",
    avatar: {
      background: "#101010",
      marks: [{ d: "M1 1", color: "#ffffff", stroke_width: 0 }],
      look: "Round glasses",
    },
  };
  const view = render(<MemberForm onSaved={onSaved} onCancel={() => {}} />);
  view.rerender(
    <MemberForm
      suggestion={suggestion}
      onSaved={onSaved}
      onCancel={() => {}}
    />,
  );
  expect(screen.getByLabelText("Name")).toHaveProperty("value", "Moss");
  expect(screen.getByLabelText(/^Personality/)).toHaveProperty(
    "value",
    "Careful and kind.",
  );
  expect(
    screen.getByText("Codex draws them once they're added: Round glasses"),
  ).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Add member" }));
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
  expect(saved().body).toMatchObject({
    name: "Moss",
    personality: "Careful and kind.",
    avatar: suggestion.avatar,
  });
});

const rune = (): Member => ({
  id: "m1",
  name: "Rune",
  kinds: ["reviewer"],
  engine: "claude",
  model: "claude-legacy-7",
  effort: "high",
  description: "Quiet and exact.",
  personality: "Terse.",
  learnings: [],
});

it("keeps a member's saved model that the engine no longer lists", async () => {
  const onSaved = vi.fn();
  render(<MemberForm member={rune()} onSaved={onSaved} onCancel={() => {}} />);
  await screen.findByRole("option", { name: "Test Sonnet (recommended)" });
  expect(choices()).toEqual([
    "The engine's default",
    "claude-legacy-7 (saved)",
    "Test Sonnet (recommended)",
    "Test Opus (Claude default)",
  ]);
  expect(screen.getByLabelText(/^Model/)).toHaveProperty(
    "value",
    "claude-legacy-7",
  );
  expect(screen.getByText(/The saved model isn't in this list/)).toBeTruthy();
  expect(screen.getByLabelText(/^Description/)).toHaveProperty(
    "value",
    "Quiet and exact.",
  );
  // Trying another engine and coming back leaves the saved model as it was.
  fireEvent.change(screen.getByLabelText("Engine"), {
    target: { value: "codex" },
  });
  await screen.findByRole("option", { name: "Test Thinker (recommended)" });
  expect(choices()).not.toContain("claude-legacy-7 (saved)");
  fireEvent.change(screen.getByLabelText("Engine"), {
    target: { value: "claude" },
  });
  await screen.findByRole("option", { name: "Test Sonnet (recommended)" });
  expect(screen.getByLabelText(/^Model/)).toHaveProperty(
    "value",
    "claude-legacy-7",
  );
  fireEvent.change(screen.getByLabelText(/^Description/), {
    target: { value: "Quiet, exact, wears a green cap." },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
  expect(saved()).toEqual({
    method: "PUT",
    body: {
      name: "Rune",
      kinds: ["reviewer"],
      engine: "claude",
      model: "claude-legacy-7",
      effort: "high",
      instructions: "",
      description: "Quiet, exact, wears a green cap.",
      personality: "Terse.",
      browser: { on: false, name: "" },
    },
  });
});

it("keeps the saved model when the list can't be found", async () => {
  catalogs.claude = {
    available: false,
    engine: "claude",
    detail: "Claude isn't signed in",
    models: [],
  };
  render(<MemberForm member={rune()} onSaved={vi.fn()} onCancel={() => {}} />);
  await screen.findByText("Claude isn't signed in");
  expect(choices()).toEqual([
    "The engine's default",
    "claude-legacy-7 (saved)",
  ]);
  expect(screen.getByLabelText(/^Model/)).toHaveProperty(
    "value",
    "claude-legacy-7",
  );
});

it("offers the engines that may run a role, whatever they are", () => {
  rememberChoices([
    ...testChoices,
    { ...testChoices[2], engine: "nova", label: "Nova", roles: true },
  ]);
  try {
    render(<MemberForm onSaved={async () => {}} onCancel={() => {}} />);
    const engine = screen.getByLabelText<HTMLSelectElement>("Engine");
    expect(Array.from(engine.options, (o) => o.textContent)).toEqual([
      "Codex",
      "Claude",
      "Nova",
    ]);
  } finally {
    rememberChoices(testChoices);
  }
});

it("keeps a saved engine that is no longer offered for roles", () => {
  const member: Member = {
    id: "m1",
    name: "Gil",
    kinds: ["reviewer"],
    engine: "grok",
    learnings: [],
  };
  render(
    <MemberForm member={member} onSaved={async () => {}} onCancel={() => {}} />,
  );
  const engine = screen.getByLabelText<HTMLSelectElement>("Engine");
  expect(engine.value).toBe("grok");
  expect(Array.from(engine.options, (o) => o.textContent)).toEqual([
    "Codex",
    "Claude",
    "Grok",
  ]);
});

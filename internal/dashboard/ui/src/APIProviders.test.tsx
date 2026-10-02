// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { AssistantForm } from "./AssistantForm";
import { MemberForm } from "./MemberForm";
import { SuggestionModel } from "./SuggestionModel";
import { TeamSettings } from "./TeamSettings";
import type { Config, Project } from "./api";
import { rememberChoices } from "./engines";
import { testChoices } from "./testEngines";
import { recordFetch, reply, type FetchCall } from "./testFetch";

rememberChoices(testChoices);

const providers = [
  { id: "openai-compatible", label: "api.openai.com" },
  { id: "openrouter", label: "OpenRouter" },
  { id: "local", label: "Local model" },
];
const catalogs: Record<string, unknown> = {
  "openai-compatible": {
    available: true,
    engine: "openai-compatible",
    provider: "openai-compatible",
    detail: "Models your endpoint lists.",
    default: { model: "", effort: "" },
    models: [{ id: "gpt-5", name: "gpt-5", efforts: [] }],
  },
  openrouter: {
    available: true,
    engine: "openai-compatible",
    provider: "openrouter",
    detail: "Models your endpoint lists.",
    default: { model: "", effort: "" },
    models: [
      {
        id: "deepseek/deepseek-r1:free",
        name: "DeepSeek R1",
        efforts: [],
        free: true,
      },
      { id: "openai/gpt-5", name: "GPT-5", efforts: [] },
    ],
  },
};
let calls: FetchCall[];
beforeEach(() => {
  calls = recordFetch((path, options) => {
    if (path === "/api/providers") return reply(providers);
    if (path.startsWith("/api/models")) {
      const provider =
        new URL(path, "http://test").searchParams.get("provider") ??
        "openai-compatible";
      return reply(catalogs[provider]);
    }
    return reply({ id: "a1", ...JSON.parse(String(options?.body ?? "{}")) });
  }).calls;
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  rememberChoices(testChoices);
});

const modelRequests = () =>
  calls.filter((c) => c.path.startsWith("/api/models")).map((c) => c.path);
const options = (label: RegExp | string) =>
  Array.from(
    screen.getByLabelText<HTMLSelectElement>(label).options,
    (o) => o.textContent,
  );

it("saves a team member on OpenRouter where the server offers API roles", async () => {
  rememberChoices(
    testChoices.map((choice) =>
      choice.engine === "openai-compatible"
        ? { ...choice, roles: true, roles_reason: "", browser: false }
        : choice,
    ),
  );
  const onSaved = vi.fn();
  render(<MemberForm onSaved={onSaved} onCancel={() => {}} />);
  fireEvent.change(screen.getByLabelText("Name"), {
    target: { value: "Rune" },
  });
  fireEvent.change(screen.getByLabelText("Engine"), {
    target: { value: "openai-compatible" },
  });
  fireEvent.change(await screen.findByLabelText("Provider"), {
    target: { value: "openrouter" },
  });
  await screen.findByRole("option", { name: "DeepSeek R1 (free)" });
  expect(screen.getByLabelText(/^Model/)).toHaveProperty("required", true);
  expect(screen.getByRole("button", { name: "Add member" })).toHaveProperty(
    "disabled",
    true,
  );
  fireEvent.change(screen.getByLabelText(/^Model/), {
    target: { value: "deepseek/deepseek-r1:free" },
  });
  expect(screen.queryByLabelText("Allow browser use")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Add member" }));
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
  const save = calls.find((call) => call.path === "/api/members")!;
  expect(JSON.parse(String(save.options?.body))).toMatchObject({
    engine: "openai-compatible",
    provider: "openrouter",
    model: "deepseek/deepseek-r1:free",
  });
});

it("runs an assistant on a provider's model, free ones marked", async () => {
  const onSaved = vi.fn();
  render(<AssistantForm onSaved={onSaved} onCancel={() => {}} />);
  fireEvent.change(screen.getByLabelText("Engine"), {
    target: { value: "openai-compatible" },
  });
  const provider = await screen.findByLabelText<HTMLSelectElement>("Provider");
  expect(options("Provider")).toEqual([
    "api.openai.com",
    "OpenRouter",
    "Local model",
  ]);
  expect(provider.value).toBe("openai-compatible");
  fireEvent.change(provider, { target: { value: "openrouter" } });
  await screen.findByRole("option", { name: "DeepSeek R1 (free)" });
  expect(options(/^Model/)).toEqual([
    "Choose a model",
    "DeepSeek R1 (free)",
    "GPT-5",
  ]);
  expect(modelRequests()).toContain(
    "/api/models?profile=assistant&engine=openai-compatible&provider=openrouter",
  );
  fireEvent.change(screen.getByLabelText(/^Model/), {
    target: { value: "deepseek/deepseek-r1:free" },
  });
  fireEvent.change(screen.getByLabelText("Name"), {
    target: { value: "Wren" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Add assistant" }));
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
  const save = calls.find((c) => c.path === "/api/assistants")!;
  expect(JSON.parse(String(save.options?.body)).model).toEqual({
    engine: "openai-compatible",
    provider: "openrouter",
    model: "deepseek/deepseek-r1:free",
    effort: "",
    max_tokens: 4096,
  });
});

it("takes any model's id typed in, and goes back to the list", async () => {
  render(<AssistantForm onSaved={async () => {}} onCancel={() => {}} />);
  fireEvent.change(screen.getByLabelText("Engine"), {
    target: { value: "openai-compatible" },
  });
  await screen.findByRole("option", { name: "gpt-5" });
  fireEvent.click(screen.getByRole("button", { name: "Enter a model id" }));
  const typed = screen.getByLabelText(/^Model/);
  expect(typed.tagName).toBe("INPUT");
  fireEvent.change(typed, { target: { value: "openai/gpt-oss-120b:free" } });
  expect(screen.getByLabelText<HTMLInputElement>(/^Model/).value).toBe(
    "openai/gpt-oss-120b:free",
  );
  fireEvent.click(screen.getByRole("button", { name: "Choose from the list" }));
  expect(screen.getByLabelText(/^Model/).tagName).toBe("SELECT");
});

it("leaves out the provider for a model on the single API setting, and asks no list of providers for a CLI", async () => {
  const onSaved = vi.fn();
  render(<AssistantForm onSaved={onSaved} onCancel={() => {}} />);
  await waitFor(() => expect(modelRequests()).toHaveLength(1));
  expect(calls.some((c) => c.path === "/api/providers")).toBe(false);
  expect(screen.queryByLabelText("Provider")).toBeNull();
  fireEvent.change(screen.getByLabelText("Engine"), {
    target: { value: "openai-compatible" },
  });
  await screen.findByRole("option", { name: "gpt-5" });
  expect(modelRequests()).toContain(
    "/api/models?profile=assistant&engine=openai-compatible",
  );
  fireEvent.change(screen.getByLabelText(/^Model/), {
    target: { value: "gpt-5" },
  });
  fireEvent.change(screen.getByLabelText("Name"), {
    target: { value: "Wren" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Add assistant" }));
  await waitFor(() => expect(onSaved).toHaveBeenCalled());
  const save = calls.find((c) => c.path === "/api/assistants")!;
  expect(JSON.parse(String(save.options?.body)).model).not.toHaveProperty(
    "provider",
  );
});

it("writes suggestions on a provider's model", async () => {
  const changed = vi.fn();
  const suggestions = {
    engine: "openai-compatible",
    provider: "openrouter",
    model: "openai/gpt-5",
    effort: "",
  };
  const config = { models: { suggestions } } as Config;
  render(<SuggestionModel config={config} onChange={changed} />);
  await screen.findByRole("option", { name: "DeepSeek R1 (free)" });
  expect(screen.getByLabelText<HTMLSelectElement>(/^Provider/).value).toBe(
    "openrouter",
  );
  expect(modelRequests()).toEqual([
    "/api/models?profile=assistant&engine=openai-compatible&provider=openrouter",
  ]);
  fireEvent.change(screen.getByLabelText(/^Provider/), {
    target: { value: "openai-compatible" },
  });
  expect(changed).toHaveBeenLastCalledWith({
    models: {
      suggestions: { engine: "openai-compatible", model: "", effort: "" },
    },
  });
  fireEvent.change(screen.getByLabelText(/^Provider/), {
    target: { value: "local" },
  });
  expect(changed).toHaveBeenLastCalledWith({
    models: {
      suggestions: {
        engine: "openai-compatible",
        provider: "local",
        model: "",
        effort: "",
      },
    },
  });
  fireEvent.click(screen.getByRole("button", { name: "Enter a model id" }));
  fireEvent.change(screen.getByLabelText(/^Model/), {
    target: { value: " meta/llama-4:free " },
  });
  expect(changed).toHaveBeenLastCalledWith({
    models: {
      suggestions: {
        ...suggestions,
        model: "meta/llama-4:free",
      },
    },
  });
});

it("says why a team member can't run on an API provider on this computer", () => {
  const reason =
    "Another API can't run team roles on this computer: commands require a sandbox the harness can prove only on macOS.";
  rememberChoices(
    testChoices.map((choice) =>
      choice.engine === "openai-compatible"
        ? { ...choice, roles_reason: reason }
        : choice,
    ),
  );
  render(<MemberForm onSaved={async () => {}} onCancel={() => {}} />);
  const engine = screen.getByLabelText<HTMLSelectElement>("Engine");
  expect(options("Engine")).toEqual([
    "Codex",
    "Claude",
    "Another API (unavailable here)",
  ]);
  const api = Array.from(engine.options).find(
    (o) => o.value === "openai-compatible",
  )!;
  expect(api.disabled).toBe(true);
  expect(screen.getByText(reason)).toBeTruthy();
});

it("keeps template seats on CLIs even when API roles are offered", () => {
  rememberChoices(
    testChoices.map((choice) =>
      choice.engine === "openai-compatible"
        ? { ...choice, roles: true, roles_reason: "" }
        : choice,
    ),
  );
  const project = {
    id: "p1",
    title: "Thanks",
    playbook: {
      template: "draft",
      medium: "documents",
      roles: [
        { name: "Writer", kinds: ["implementer"], engine: "claude" },
        { name: "Reviewer", kinds: ["reviewer"], engine: "codex" },
      ],
      max_rounds: 3,
      deliver: "owner",
    },
  } as unknown as Project;
  render(
    <TeamSettings project={project} members={[]} refresh={async () => {}} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  for (const role of ["Writer", "Reviewer"]) {
    const slot = screen.getByRole("group", { name: role });
    const engine = within(slot).getByLabelText<HTMLSelectElement>("Engine");
    expect(Array.from(engine.options, (o) => o.textContent)).toEqual([
      "Codex",
      "Claude",
    ]);
    expect(
      Array.from(engine.options).some((o) => o.value === "openai-compatible"),
    ).toBe(false);
  }
});

it.each(["", "   "])(
  "refuses a blank typed API member model (%j)",
  async (model) => {
    rememberChoices(
      testChoices.map((choice) =>
        choice.engine === "openai-compatible"
          ? { ...choice, roles: true, roles_reason: "" }
          : choice,
      ),
    );
    render(<MemberForm onSaved={vi.fn()} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Rune" },
    });
    fireEvent.change(screen.getByLabelText("Engine"), {
      target: { value: "openai-compatible" },
    });
    await screen.findByRole("option", { name: "gpt-5" });
    fireEvent.click(screen.getByRole("button", { name: "Enter a model id" }));
    const field = screen.getByLabelText(/^Model/);
    expect(field).toHaveProperty("required", true);
    fireEvent.change(field, { target: { value: model } });
    expect(screen.getByRole("button", { name: "Add member" })).toHaveProperty(
      "disabled",
      true,
    );
    fireEvent.submit(field.closest("form")!);
    expect(calls.some((call) => call.path === "/api/members")).toBe(false);
  },
);

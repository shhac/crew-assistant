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
import { EngineSettings } from "./EngineSettings";
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

it("keeps named API providers beside the single API setting", () => {
  const changed = vi.fn();
  const config = {
    engines: { "openai-compatible": { base_url: "https://api.x.ai/v1" } },
  } as Config;
  const view = render(<EngineSettings config={config} onChange={changed} />);
  fireEvent.click(screen.getByRole("button", { name: "Add an API provider" }));
  const added = changed.mock.lastCall![0] as Config;
  expect(added.engines).toEqual({
    "openai-compatible": { base_url: "https://api.x.ai/v1" },
    providers: [{ id: "", name: "", base_url: "", api_key_env: "" }],
  });
  view.rerender(<EngineSettings config={added} onChange={changed} />);
  fireEvent.change(screen.getByLabelText("Name"), {
    target: { value: "Open Router" },
  });
  const named = changed.mock.lastCall![0] as Config;
  expect(named.engines!.providers).toEqual([
    { id: "open-router", name: "Open Router", base_url: "", api_key_env: "" },
  ]);
  view.rerender(<EngineSettings config={named} onChange={changed} />);
  fireEvent.change(screen.getByLabelText(/^Id/), {
    target: { value: "openrouter" },
  });
  const id = changed.mock.lastCall![0] as Config;
  view.rerender(<EngineSettings config={id} onChange={changed} />);
  // Once changed by hand, the id no longer follows the name.
  fireEvent.change(screen.getByLabelText("Name"), {
    target: { value: "OpenRouter" },
  });
  const renamed = changed.mock.lastCall![0] as Config;
  view.rerender(<EngineSettings config={renamed} onChange={changed} />);
  fireEvent.change(screen.getAllByLabelText("API address")[1], {
    target: { value: "https://openrouter.ai/api/v1" },
  });
  const addressed = changed.mock.lastCall![0] as Config;
  view.rerender(<EngineSettings config={addressed} onChange={changed} />);
  fireEvent.change(screen.getAllByLabelText(/^API key variable/)[1], {
    target: { value: "OPENROUTER_API_KEY" },
  });
  const keyed = changed.mock.lastCall![0] as Config;
  view.rerender(<EngineSettings config={keyed} onChange={changed} />);
  fireEvent.change(
    screen.getAllByLabelText(/^Reasoning effort is sent as/)[1],
    { target: { value: "reasoning.effort" } },
  );
  const saved = changed.mock.lastCall![0] as Config;
  expect(saved.engines).toEqual({
    "openai-compatible": { base_url: "https://api.x.ai/v1" },
    providers: [
      {
        id: "openrouter",
        name: "OpenRouter",
        base_url: "https://openrouter.ai/api/v1",
        api_key_env: "OPENROUTER_API_KEY",
        effort_parameter: "reasoning.effort",
      },
    ],
  });
  view.rerender(<EngineSettings config={saved} onChange={changed} />);
  fireEvent.click(
    screen.getByRole("button", { name: "Remove API provider OpenRouter" }),
  );
  expect(changed).toHaveBeenLastCalledWith({
    engines: { "openai-compatible": { base_url: "https://api.x.ai/v1" } },
  });
});

it("says why a team member can't run on an API provider yet", () => {
  const reason =
    "An API provider can't run team roles yet: it has no sandboxed workspace tools.";
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
    "Another API (not for team roles yet)",
  ]);
  const api = Array.from(engine.options).find(
    (o) => o.value === "openai-compatible",
  )!;
  expect(api.disabled).toBe(true);
  expect(screen.getByText(reason)).toBeTruthy();
});

it("says why a template's role can't run on an API provider yet, on the Team settings", () => {
  const reason =
    "An API provider can't run team roles yet: it has no sandboxed workspace tools.";
  rememberChoices(
    testChoices.map((choice) =>
      choice.engine === "openai-compatible"
        ? { ...choice, roles_reason: reason }
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
      "Another API (not for team roles yet)",
    ]);
    expect(
      Array.from(engine.options).find((o) => o.value === "openai-compatible")!
        .disabled,
    ).toBe(true);
    const hint = within(slot).getByText(reason);
    expect(engine.getAttribute("aria-describedby")).toBe(hint.id);
  }
});

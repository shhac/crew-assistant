// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { SuggestionModel } from "./SuggestionModel";
import type { Config } from "./api";
import { rememberChoices } from "./engines";
import { testChoices } from "./testEngines";

rememberChoices(testChoices);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
const suggestions = {
  engine: "codex",
  model: "thinker",
  effort: "high",
};
const engines = {
  codex: { home: "/test/login", usage_floor: { "5h_percent": 20 } },
  claude: { bin: "/test/claude" },
};
const config = { models: { suggestions }, engines } as Config;
const smallModels = {
  models: { suggestions: { engine: "", model: "", effort: "" } },
  engines,
} as Config;
const catalog = {
  available: true,
  engine: "codex",
  detail: "Reported by Codex",
  default: { model: "thinker", effort: "high" },
  models: [
    {
      id: "thinker",
      name: "Test Thinker",
      default_effort: "high",
      efforts: [{ id: "low" }, { id: "high" }],
    },
    {
      id: "builder",
      name: "Test Builder",
      default_effort: "low",
      efforts: [{ id: "low" }],
      is_default: true,
    },
  ],
};
function mockCatalog(data: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({ ok: true, json: async () => data }),
  );
}
it("uses the small models unless the owner picks another, and lists none for them", () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const changed = vi.fn();
  render(<SuggestionModel config={smallModels} onChange={changed} />);
  const engine = screen.getByLabelText<HTMLSelectElement>(
    /^Suggestions and loading lines/,
  );
  expect(engine.value).toBe("");
  expect(Array.from(engine.options, (o) => o.textContent)).toEqual([
    "The small models (recommended)",
    "A model on Codex",
    "A model on Claude",
    "A model on another API",
  ]);
  expect(screen.getByText(/Luna on your Codex login, or Haiku/)).toBeTruthy();
  expect(screen.queryByLabelText("Model")).toBeNull();
  expect(fetch).not.toHaveBeenCalled();
  fireEvent.change(engine, { target: { value: "claude" } });
  expect(changed).toHaveBeenCalledWith({
    ...smallModels,
    models: { suggestions: { engine: "claude", model: "", effort: "" } },
  });
});
it("takes a model on another API as typed while its list offers none", async () => {
  mockCatalog({
    available: false,
    engine: "openai-compatible",
    detail: "The API didn't list its models",
    models: [],
  });
  const changed = vi.fn();
  const api = {
    ...config,
    models: {
      suggestions: { engine: "openai-compatible", model: "", effort: "" },
    },
  };
  render(<SuggestionModel config={api} onChange={changed} />);
  await screen.findByText("The API didn't list its models");
  const model = screen.getByLabelText(/^Model/);
  expect(model.tagName).toBe("INPUT");
  expect(screen.getByText(/billed by the API/)).toBeTruthy();
  expect(fetch).toHaveBeenCalledWith(
    "/api/models?profile=assistant&engine=openai-compatible",
    expect.anything(),
  );
  fireEvent.change(model, { target: { value: " local-small " } });
  expect(changed).toHaveBeenLastCalledWith({
    ...api,
    models: {
      suggestions: {
        engine: "openai-compatible",
        model: "local-small",
        effort: "",
      },
    },
  });
  fireEvent.change(screen.getByLabelText(/^Reasoning effort/), {
    target: { value: "low" },
  });
  expect(changed).toHaveBeenLastCalledWith({
    ...api,
    models: {
      suggestions: { engine: "openai-compatible", model: "", effort: "low" },
    },
  });
});
it("offers the models another API lists, with any effort typed in", async () => {
  mockCatalog({
    available: true,
    engine: "openai-compatible",
    detail: "Listed by the API",
    default: { model: "", effort: "" },
    models: [
      { id: "small-a", name: "small-a", efforts: [], efforts_known: false },
      { id: "small-b", name: "small-b", efforts: [], efforts_known: false },
    ],
  });
  const changed = vi.fn();
  const api = {
    ...config,
    models: {
      suggestions: {
        engine: "openai-compatible",
        model: "small-a",
        effort: "",
      },
    },
  };
  render(<SuggestionModel config={api} onChange={changed} />);
  await screen.findByRole("option", { name: "small-b" });
  expect(screen.getByLabelText(/^Model/).tagName).toBe("SELECT");
  expect(screen.getByLabelText(/^Reasoning effort/).tagName).toBe("INPUT");
  fireEvent.change(screen.getByLabelText(/^Reasoning effort/), {
    target: { value: "minimal" },
  });
  expect(changed).toHaveBeenLastCalledWith({
    ...api,
    models: {
      suggestions: {
        engine: "openai-compatible",
        model: "small-a",
        effort: "minimal",
      },
    },
  });
});
it("offers only engines that may run small jobs", () => {
  vi.stubGlobal("fetch", vi.fn());
  rememberChoices([
    ...testChoices,
    { ...testChoices[2], engine: "nova", label: "Nova", small: true },
  ]);
  try {
    render(<SuggestionModel config={smallModels} onChange={() => {}} />);
    const engine = screen.getByLabelText<HTMLSelectElement>(
      /^Suggestions and loading lines/,
    );
    expect(Array.from(engine.options, (o) => o.textContent)).toEqual([
      "The small models (recommended)",
      "A model on Codex",
      "A model on Claude",
      "A model on another API",
      "A model on Nova",
    ]);
  } finally {
    rememberChoices(testChoices);
  }
});
it("offers any model its login lists, with only its supported efforts", async () => {
  mockCatalog(catalog);
  const changed = vi.fn();
  render(<SuggestionModel config={config} onChange={changed} />);
  await screen.findByRole("option", { name: "Test Thinker (recommended)" });
  expect(screen.getByLabelText("Model").tagName).toBe("SELECT");
  expect(
    screen.getByRole("option", { name: "Test Builder (Codex default)" }),
  ).toBeTruthy();
  expect(screen.getByRole("option", { name: "high (default)" })).toBeTruthy();
  expect(screen.getByRole("status").textContent).toBe("Reported by Codex");
  expect(screen.queryByRole("option", { name: "ultra" })).toBeNull();
  expect(fetch).toHaveBeenCalledWith(
    "/api/models?profile=assistant&engine=codex",
    expect.anything(),
  );
  expect(changed).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("Model"), {
    target: { value: "builder" },
  });
  expect(changed).toHaveBeenCalledWith({
    ...config,
    models: {
      suggestions: { ...suggestions, model: "builder", effort: "low" },
    },
  });
});
it("keeps a saved model the login no longer lists until another is picked", async () => {
  mockCatalog(catalog);
  const custom = {
    ...config,
    models: {
      suggestions: { ...suggestions, model: "custom-model", effort: "ultra" },
    },
  };
  const changed = vi.fn();
  render(<SuggestionModel config={custom} onChange={changed} />);
  await screen.findByText(
    "Your saved model isn't in this list. It stays until you pick another.",
  );
  expect(screen.getByLabelText<HTMLSelectElement>("Model").value).toBe(
    "custom-model",
  );
  expect(screen.getByRole("option", { name: "ultra (saved)" })).toBeTruthy();
  expect(changed).not.toHaveBeenCalled();
});
it("keeps the saved choice and offers a refresh when the model list cannot load", async () => {
  const fetch = vi
    .fn()
    .mockRejectedValueOnce(new Error("offline"))
    .mockResolvedValue({ ok: true, json: async () => catalog });
  vi.stubGlobal("fetch", fetch);
  const changed = vi.fn();
  render(<SuggestionModel config={config} onChange={changed} />);
  expect(screen.getByRole("status").textContent).toBe("Finding models…");
  await screen.findByText(
    "Couldn't load the list of models. Your choice hasn't changed.",
  );
  expect(screen.getByLabelText<HTMLSelectElement>("Model").value).toBe(
    "thinker",
  );
  fireEvent.click(screen.getByRole("button", { name: "Refresh the list" }));
  await screen.findByRole("option", { name: "Test Thinker (recommended)" });
  expect(fetch).toHaveBeenCalledTimes(2);
  expect(changed).not.toHaveBeenCalled();
});

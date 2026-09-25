// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ModelSettings } from "./ModelSettings";
import type { Config } from "./api";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
const model = {
  engine: "codex",
  model: "thinker",
  effort: "high",
};
const engines = {
  codex: { home: "/test/login", usage_floor: { "5h_percent": 20 } },
  claude: { bin: "/test/claude" },
};
const config = { model, engines } as Config;
const defaults = {
  engines: {
    codex: { bin: "codex", home: "/test/default-codex" },
    claude: { bin: "claude", home: "/test/default-claude" },
  },
  usage_floor: 10,
  on_unknown_usage: "allow" as const,
  openai_base_url: "https://api.example.test/v1",
};
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
it("uses discovered friendly models and only their supported efforts", async () => {
  mockCatalog(catalog);
  const changed = vi.fn();
  render(<ModelSettings config={config} onChange={changed} />);
  await screen.findByRole("option", { name: "Test Thinker (recommended)" });
  expect(screen.getByLabelText("Model").tagName).toBe("SELECT");
  expect(
    screen.getByRole("option", { name: "Test Builder (Codex default)" }),
  ).toBeTruthy();
  expect(
    screen.getByRole("option", { name: "The model's default (high)" }),
  ).toBeTruthy();
  expect(screen.getByRole("option", { name: "high (default)" })).toBeTruthy();
  expect(screen.getByRole("option", { name: "low" })).toBeTruthy();
  expect(screen.getByRole("status").textContent).toBe("Reported by Codex");
  expect(screen.queryByRole("option", { name: "ultra" })).toBeNull();
  expect(changed).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("Model"), {
    target: { value: "builder" },
  });
  expect(changed).toHaveBeenCalledWith({
    ...config,
    model: { ...model, model: "builder", effort: "low" },
  });
  expect(document.querySelector("details")?.open).toBe(false);
});
it("preserves a saved custom model and effort when discovery is unavailable", async () => {
  mockCatalog({
    available: false,
    engine: "codex",
    detail: "Login unavailable; saved settings unchanged",
    models: [],
  });
  const changed = vi.fn();
  render(<ModelSettings config={config} onChange={changed} />);
  await screen.findByText("Login unavailable; saved settings unchanged");
  expect(screen.getByLabelText<HTMLSelectElement>("Model").value).toBe(
    "thinker",
  );
  expect(screen.getByRole("option", { name: "thinker (saved)" })).toBeTruthy();
  expect(
    screen.getByLabelText<HTMLSelectElement>("Reasoning effort", {
      selector: "select",
    }).value,
  ).toBe("high");
  expect(
    screen.getByLabelText<HTMLInputElement>("Reasoning effort", {
      selector: "input",
    }).value,
  ).toBe("high");
  expect(screen.getByLabelText<HTMLInputElement>("Model ID").value).toBe(
    "thinker",
  );
  expect(changed).not.toHaveBeenCalled();
});
it("keeps unknown saved model after successful discovery until an explicit selection", async () => {
  mockCatalog(catalog);
  const changed = vi.fn();
  const custom = {
    ...config,
    model: { ...model, model: "custom-model", effort: "ultra" },
  };
  render(<ModelSettings config={custom} onChange={changed} />);
  await screen.findByText(
    "Your saved model isn't in this list. It stays until you pick another.",
  );
  expect(screen.getByLabelText<HTMLSelectElement>("Model").value).toBe(
    "custom-model",
  );
  expect(
    screen.getByRole("option", { name: "custom-model (saved)" }),
  ).toBeTruthy();
  expect(
    screen.getByLabelText<HTMLSelectElement>("Reasoning effort", {
      selector: "select",
    }).value,
  ).toBe("ultra");
  expect(screen.getByRole("option", { name: "ultra (saved)" })).toBeTruthy();
  expect(changed).not.toHaveBeenCalled();
});
it("lists Claude models and effort choices from CLI initialization", async () => {
  mockCatalog({
    ...catalog,
    engine: "claude",
    models: [
      {
        id: "opus",
        name: "Opus",
        default_effort: "",
        efforts: [{ id: "high" }, { id: "max" }],
      },
      {
        id: "sonnet",
        name: "Sonnet",
        default_effort: "",
        efforts: [{ id: "high" }],
        is_default: true,
      },
    ],
  });
  const changed = vi.fn();
  render(
    <ModelSettings
      config={
        { model: { ...model, engine: "claude", model: "opus" } } as Config
      }
      onChange={changed}
    />,
  );
  await screen.findByRole("option", { name: "Opus" });
  expect(
    screen.getByRole("option", { name: "Sonnet (Claude default)" }),
  ).toBeTruthy();
  expect(screen.getByRole("option", { name: "max" })).toBeTruthy();
  expect(screen.queryByRole("option", { name: "ultra" })).toBeNull();
  expect(fetch).toHaveBeenCalledWith(
    "/api/models?profile=assistant&engine=claude",
    expect.anything(),
  );
  expect(changed).not.toHaveBeenCalled();
});
it("switches CLI engines without retaining an incompatible model identifier", async () => {
  mockCatalog(catalog);
  const changed = vi.fn();
  render(<ModelSettings config={config} onChange={changed} />);
  await screen.findByRole("option", { name: "Test Thinker (recommended)" });
  expect(screen.getByRole("option", { name: "Codex" })).toBeTruthy();
  expect(screen.getByRole("option", { name: "Claude" })).toBeTruthy();
  expect(screen.getByRole("option", { name: "Another API" })).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Runs on"), {
    target: { value: "claude" },
  });
  expect(changed).toHaveBeenCalledWith({
    ...config,
    model: { ...model, engine: "claude", model: "", effort: "" },
  });
});

it("keeps the saved choice and offers a refresh when the model list cannot load", async () => {
  const fetch = vi
    .fn()
    .mockRejectedValueOnce(new Error("offline"))
    .mockResolvedValue({ ok: true, json: async () => catalog });
  vi.stubGlobal("fetch", fetch);
  const changed = vi.fn();
  render(<ModelSettings config={config} onChange={changed} />);
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
it("labels the advanced settings for each engine and fetches no list for an API", () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const changed = vi.fn();
  const view = render(<ModelSettings config={config} onChange={changed} />);
  expect(screen.getByText("More model settings").tagName).toBe("SUMMARY");
  expect(screen.getByLabelText<HTMLInputElement>(/^Codex folder/).value).toBe(
    "/test/login",
  );
  expect(screen.getByLabelText("Codex program")).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Codex program"), {
    target: { value: "/bin/codex" },
  });
  expect(changed).toHaveBeenLastCalledWith({
    ...config,
    engines: { ...engines, codex: { ...engines.codex, bin: "/bin/codex" } },
  });
  view.rerender(
    <ModelSettings
      config={{ model: { engine: "claude" } }}
      onChange={changed}
    />,
  );
  expect(screen.getByLabelText("Claude program")).toBeTruthy();
  expect(screen.getByLabelText(/^Claude settings folder/)).toBeTruthy();
  fetch.mockClear();
  view.rerender(
    <ModelSettings
      config={{ model: { engine: "openai-compatible", max_tokens: 4096 } }}
      onChange={changed}
    />,
  );
  expect(screen.queryByLabelText("Model")).toBeNull();
  expect(screen.getByLabelText("API address")).toBeTruthy();
  expect(screen.getByLabelText(/^API key variable/)).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Most output tokens per call"), {
    target: { value: "8192" },
  });
  expect(changed).toHaveBeenLastCalledWith({
    model: { engine: "openai-compatible", max_tokens: 8192 },
  });
  expect(fetch).not.toHaveBeenCalled();
});
it("shows what a blank engine setting falls back to", () => {
  vi.stubGlobal("fetch", vi.fn());
  const view = render(
    <ModelSettings
      config={{ model: { engine: "codex" } }}
      defaults={defaults}
      onChange={() => {}}
    />,
  );
  expect(
    screen.getByLabelText<HTMLInputElement>("Codex program").placeholder,
  ).toBe("codex");
  expect(
    screen.getByLabelText<HTMLInputElement>(/^Codex folder/).placeholder,
  ).toBe("/test/default-codex");
  view.rerender(
    <ModelSettings
      config={{ model: { engine: "claude" } }}
      defaults={defaults}
      onChange={() => {}}
    />,
  );
  expect(
    screen.getByLabelText<HTMLInputElement>("Claude program").placeholder,
  ).toBe("claude");
  expect(
    screen.getByLabelText<HTMLInputElement>(/^Claude settings folder/)
      .placeholder,
  ).toBe("/test/default-claude");
  view.rerender(
    <ModelSettings
      config={{ model: { engine: "openai-compatible" } }}
      defaults={defaults}
      onChange={() => {}}
    />,
  );
  expect(
    screen.getByLabelText<HTMLInputElement>("API address").placeholder,
  ).toBe("https://api.example.test/v1");
});
it("clears a blanked engine setting so the default applies, keeping the other engine's", () => {
  vi.stubGlobal("fetch", vi.fn());
  const changed = vi.fn();
  const claude = { ...config, model: { engine: "claude" } };
  render(<ModelSettings config={claude} onChange={changed} />);
  fireEvent.change(screen.getByLabelText("Claude program"), {
    target: { value: "" },
  });
  expect(changed).toHaveBeenLastCalledWith({
    ...claude,
    engines: { codex: engines.codex, claude: {} },
  });
  fireEvent.change(screen.getByLabelText(/^Claude settings folder/), {
    target: { value: "/test/claude-home" },
  });
  expect(changed).toHaveBeenLastCalledWith({
    ...claude,
    engines: {
      codex: engines.codex,
      claude: { bin: "/test/claude", home: "/test/claude-home" },
    },
  });
});
it("writes the API address and key variable to the API engine", () => {
  vi.stubGlobal("fetch", vi.fn());
  const changed = vi.fn();
  const api = {
    model: { engine: "openai-compatible", model: "m" },
    engines: {
      ...engines,
      "openai-compatible": { base_url: "", api_key_env: "" },
    },
  };
  render(<ModelSettings config={api} onChange={changed} />);
  fireEvent.change(screen.getByLabelText(/^API key variable/), {
    target: { value: "TEST_KEY" },
  });
  expect(changed).toHaveBeenLastCalledWith({
    ...api,
    engines: { ...engines, "openai-compatible": { api_key_env: "TEST_KEY" } },
  });
});

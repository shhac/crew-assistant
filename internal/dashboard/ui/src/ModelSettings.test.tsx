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
  codex_home: "/test/login",
};
const config = { model } as Config;
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
  await screen.findByRole("option", { name: "Test Thinker — recommended" });
  expect(screen.getByLabelText("Assistant model").tagName).toBe("SELECT");
  expect(screen.queryByRole("option", { name: "ultra" })).toBeNull();
  expect(changed).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("Assistant model"), {
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
  expect(
    (screen.getByLabelText("Assistant model") as HTMLSelectElement).value,
  ).toBe("thinker");
  expect(
    (screen.getByLabelText("Assistant reasoning effort") as HTMLSelectElement)
      .value,
  ).toBe("high");
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
  await screen.findByText(/Your saved model is not in this catalog/);
  expect(
    (screen.getByLabelText("Assistant model") as HTMLSelectElement).value,
  ).toBe("custom-model");
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
  await screen.findByRole("option", { name: "Test Thinker — recommended" });
  fireEvent.change(screen.getByLabelText("Assistant engine"), {
    target: { value: "claude" },
  });
  expect(changed).toHaveBeenCalledWith({
    ...config,
    model: { ...model, engine: "claude", model: "", effort: "" },
  });
});

// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ChatSettings } from "./ChatSettings";
import type { AssistantProfile, Config } from "./api";
import { rememberChoices } from "./engines";
import { testChoices } from "./testEngines";

rememberChoices(testChoices);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
const on = (engine: string) =>
  ({
    assistant: { seat: "milo" },
    assistants: [
      {
        id: "milo",
        name: "Milo",
        personality: "",
        model: { engine, model: "", effort: "", max_tokens: 4096 },
      } as AssistantProfile,
    ],
  }) as Config;
const config = {
  ...on("codex"),
  engines: { codex: { home: "/synthetic/login" } },
  chat: {
    other: "preserved",
    loading_phrases: { enabled: true },
  },
} as Config;
const hint = () =>
  screen.getByRole("link", { name: "Choose the model" }).parentElement!
    .textContent;
it("toggles loading phrases without fetching a model catalog", () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const changed = vi.fn();
  render(<ChatSettings config={config} onChange={changed} />);
  const toggle = screen.getByRole<HTMLInputElement>("checkbox", {
    name: "Show a line while it works",
  });
  expect(toggle.checked).toBe(true);
  expect(
    document.getElementById(toggle.getAttribute("aria-describedby")!)
      ?.textContent,
  ).toBe(
    "A small model writes it from the last two messages. It counts toward your daily model calls.",
  );
  expect(fetch).not.toHaveBeenCalled();
  fireEvent.click(toggle);
  expect(changed).toHaveBeenCalledWith({
    ...config,
    chat: { other: "preserved", loading_phrases: { enabled: false } },
  });
});
it("names the seated assistant's CLI's small model first and the other as fallback, and where to choose another", () => {
  const view = render(<ChatSettings config={config} onChange={() => {}} />);
  expect(hint()).toBe(
    "Loading messages and next-message suggestions use your Codex login (gpt-6-luna, low effort), or your Claude login (haiku, low effort) if that isn't working. No other model is used. Choose the model",
  );
  expect(
    screen.getByRole("link", { name: "Choose the model" }).getAttribute("href"),
  ).toBe("#/settings/models");
  expect(screen.queryByRole("combobox")).toBeNull();
  expect(screen.queryByText(/gpt-5\.6-luna/)).toBeNull();
  view.rerender(
    <ChatSettings
      config={{ ...config, ...on("claude") }}
      onChange={() => {}}
    />,
  );
  expect(hint()).toMatch(
    /^Loading messages and next-message suggestions use your Claude login \(haiku, low effort\), or your Codex login \(gpt-6-luna, low effort\) if that isn't working\./,
  );
});
it("names the model the owner chose in Models", () => {
  render(
    <ChatSettings
      config={{
        ...config,
        models: {
          suggestions: { engine: "claude", model: "opus", effort: "" },
        },
      }}
      onChange={() => {}}
    />,
  );
  expect(hint()).toMatch(
    /^Loading messages and next-message suggestions use opus on your Claude login\. No other model is used\./,
  );
});
it("names a model the owner chose on another API, and that it bills", () => {
  render(
    <ChatSettings
      config={{
        ...config,
        models: {
          suggestions: {
            engine: "openai-compatible",
            model: "local-small",
            effort: "",
          },
        },
      }}
      onChange={() => {}}
    />,
  );
  expect(hint()).toMatch(
    /^Loading messages and next-message suggestions use local-small on your API, billed per call\. No other model is used\./,
  );
});
it("makes no small-model requests for API assistants", () => {
  render(
    <ChatSettings
      config={{ ...config, ...on("openai-compatible") }}
      onChange={() => {}}
    />,
  );
  expect(hint()).toMatch(
    /^The assistant uses an API, so loading messages are a fixed line and cost nothing extra\./,
  );
  expect(screen.queryByText(/gpt-6-luna|haiku/)).toBeNull();
});

// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ChatSettings } from "./ChatSettings";
import type { Config } from "./api";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
const config = {
  model: { engine: "codex", codex_home: "/synthetic/login" },
  chat: {
    other: "preserved",
    loading_phrases: { enabled: true },
  },
} as Config;
it("toggles loading phrases without fetching a model catalog", () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const changed = vi.fn();
  render(<ChatSettings config={config} onChange={changed} />);
  const toggle = screen.getByRole<HTMLInputElement>("checkbox", {
    name: "Loading messages",
  });
  expect(toggle.checked).toBe(true);
  expect(fetch).not.toHaveBeenCalled();
  fireEvent.click(toggle);
  expect(changed).toHaveBeenCalledWith({
    ...config,
    chat: { other: "preserved", loading_phrases: { enabled: false } },
  });
});
it("names the own CLI's approved model first and the other as fallback, with no model choice", () => {
  const view = render(<ChatSettings config={config} onChange={() => {}} />);
  expect(
    screen.getByText(
      "Loading messages and next-message suggestions use your Codex login (gpt-6-luna, low effort), or your Claude login (haiku) if that isn't working. No other model is used.",
    ),
  ).toBeTruthy();
  expect(screen.queryByRole("combobox")).toBeNull();
  expect(screen.queryByText(/gpt-5\.6-luna/)).toBeNull();
  view.rerender(
    <ChatSettings
      config={{ ...config, model: { engine: "claude" } }}
      onChange={() => {}}
    />,
  );
  expect(
    screen.getByText(
      "Loading messages and next-message suggestions use your Claude login (haiku), or your Codex login (gpt-6-luna, low effort) if that isn't working. No other model is used.",
    ),
  ).toBeTruthy();
});
it("makes no small-model requests for API assistants", () => {
  render(
    <ChatSettings
      config={{ ...config, model: { engine: "openai-compatible" } }}
      onChange={() => {}}
    />,
  );
  expect(
    screen.getByText(
      "The assistant uses an API, so loading messages are a fixed line and cost nothing extra.",
    ),
  ).toBeTruthy();
  expect(screen.queryByText(/gpt-6-luna|haiku/)).toBeNull();
});

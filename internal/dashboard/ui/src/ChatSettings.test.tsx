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
  expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(true);
  expect(fetch).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("checkbox"));
  expect(changed).toHaveBeenCalledWith({
    ...config,
    chat: { other: "preserved", loading_phrases: { enabled: false } },
  });
});
it("names the own CLI's approved model first and the other as fallback, with no model choice", () => {
  const view = render(<ChatSettings config={config} onChange={() => {}} />);
  expect(
    screen.getByText(
      /your Codex CLI login \(gpt-6-luna, low effort\).*your Claude login \(haiku\) instead/,
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
      /your Claude CLI login \(haiku\).*your Codex login \(gpt-6-luna, low effort\) instead/,
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
  expect(screen.getByText(/make no additional model requests/)).toBeTruthy();
  expect(screen.queryByText(/gpt-6-luna|haiku/)).toBeNull();
});

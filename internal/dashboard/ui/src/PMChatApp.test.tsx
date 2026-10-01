// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { App } from "./App";
import { normalizeState, type Project } from "./api";
import { rememberChoices } from "./engines";
import { testChoices } from "./testEngines";
rememberChoices(testChoices);
const project = (id: string): Project => ({
  id,
  title: `Project ${id}`,
  status: "active",
  brief: { version: 1, goal: "A project", criteria: [] },
  playbook: {
    template: "draft",
    medium: "documents",
    roles: [{ name: `PM ${id}`, kinds: ["pm"], engine: "claude" }],
    deliver: "owner",
    max_rounds: 3,
  },
});
const go = (hash: string) => {
  window.location.hash = hash;
  window.dispatchEvent(new HashChangeEvent("hashchange"));
};
beforeEach(() => {
  window.localStorage.clear();
  window.history.replaceState(null, "", "/#/projects/a/pm");
  const state = normalizeState({
    assistant: { name: "Iris", personality: "Helpful" },
    projects: [project("a"), project("b")],
  });
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => ({
      ok: true,
      status: 200,
      json: async () =>
        path === "/api/state"
          ? state
          : path.endsWith("/pm-chat")
            ? { messages: [] }
            : {},
    })),
  );
  HTMLDialogElement.prototype.showModal = function () {
    this.setAttribute("open", "");
  };
  HTMLDialogElement.prototype.close = function () {
    this.removeAttribute("open");
  };
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("keeps distinct project drafts across navigation and the assistant draft beside them", async () => {
  render(<App />);
  const a = await screen.findByPlaceholderText("Message PM a about Project a");
  fireEvent.change(a, { target: { value: "Draft for a" } });
  const assistant = screen.getByLabelText("Message Iris");
  fireEvent.change(assistant, { target: { value: "Assistant draft" } });
  go("#/projects/b/pm");
  const b = await screen.findByPlaceholderText("Message PM b about Project b");
  expect(b).toHaveProperty("value", "");
  fireEvent.change(b, { target: { value: "Draft for b" } });
  go("#/projects/a/pm");
  expect(
    await screen.findByPlaceholderText("Message PM a about Project a"),
  ).toHaveProperty("value", "Draft for a");
  go("#/projects/b/pm");
  expect(
    await screen.findByPlaceholderText("Message PM b about Project b"),
  ).toHaveProperty("value", "Draft for b");
  expect(screen.getByLabelText("Message Iris")).toHaveProperty(
    "value",
    "Assistant draft",
  );
});
it("Escape in the PM large editor keeps the assistant drawer open", async () => {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query.includes("max-width"),
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
  render(<App />);
  await screen.findByPlaceholderText("Message PM a about Project a");
  fireEvent.click(screen.getByRole("button", { name: /^Chat/ }));
  const drawer = await screen.findByRole("dialog", { name: "Chat" });
  const pm = screen.getByRole("region", { name: "PM chat" });
  fireEvent.change(within(pm).getByRole("textbox"), {
    target: { value: "Long PM draft" },
  });
  fireEvent.click(
    within(pm).getByRole("button", { name: "Write in a larger space" }),
  );
  const editor = screen.getByRole("dialog", { name: "Write a message" });
  const field = within(editor).getByRole("textbox");
  expect(document.activeElement).toBe(field);
  fireEvent.keyDown(field, { key: "Escape" });
  await waitFor(() =>
    expect(
      screen.queryByRole("dialog", { name: "Write a message" }),
    ).toBeNull(),
  );
  expect(screen.getByRole("dialog", { name: "Chat" })).toBe(drawer);
  expect(within(pm).getByRole("textbox")).toHaveProperty(
    "value",
    "Long PM draft",
  );
});

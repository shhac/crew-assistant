// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { App } from "./App";
import { bootstrapSession, normalizeState, type State } from "./api";

const initial = (): State =>
  normalizeState({
    assistant: { name: "Iris", personality: "Concise and thoughtful." },
  });
let state: State;
let calls: { path: string; options?: RequestInit }[];
let respond: (
  path: string,
  options?: RequestInit,
) => { status?: number; body?: unknown };
beforeEach(() => {
  state = initial();
  calls = [];
  window.history.replaceState(null, "", "/");
  window.localStorage.clear();
  delete document.documentElement.dataset.theme;
  respond = (path) => ({ body: path === "/api/state" ? state : {} });
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, options?: RequestInit) => {
      calls.push({ path: input, options });
      const result = respond(input, options);
      const status = result.status || 200;
      return {
        ok: status >= 200 && status < 300,
        status,
        json: async () => result.body ?? {},
      };
    }),
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

const go = (hash: string) => {
  window.location.hash = hash;
  window.dispatchEvent(new HashChangeEvent("hashchange"));
};
const writes = () =>
  calls.filter((c) => c.options?.method && c.options.method !== "GET");

const project = {
  id: "p1",
  title: "Launch note",
  status: "active",
  brief: { version: 1, goal: "Tell customers", criteria: [] },
  directories: [],
};

describe("the shell", () => {
  it("starts on an inbox with one way in when there is nothing yet", async () => {
    render(<App />);
    expect(await screen.findByRole("heading", { name: "Inbox" })).toBeTruthy();
    expect(screen.getByText("No projects yet.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "New project" })).toBeTruthy();
    const nav = screen.getByRole("navigation", { name: "Main" });
    for (const name of ["Inbox", "Projects", "Memory", "Settings"])
      expect(
        within(nav).getByRole("link", { name: new RegExp(`^${name}`) }),
      ).toBeTruthy();
    expect(screen.getByLabelText("Message Iris")).toBeTruthy();
    expect(screen.queryByText(/^Demo:/)).toBeNull();
  });
  it("shows the assistant's avatar as the favicon and in the navigation", async () => {
    const icon = document.createElement("link");
    icon.rel = "icon";
    icon.href = "data:,";
    document.head.append(icon);
    state.assistant.avatar_svg = '<svg xmlns="http://www.w3.org/2000/svg"/>';
    const url = `data:image/svg+xml,${encodeURIComponent(state.assistant.avatar_svg)}`;
    render(<App />);
    const nav = await screen.findByRole("navigation", { name: "Main" });
    const brand = within(nav).getByRole("link", { name: "Iris" });
    expect(brand.querySelector("img")?.getAttribute("src")).toBe(url);
    expect(brand.textContent).toBe("Iris");
    await waitFor(() => expect(icon.getAttribute("href")).toBe(url));
    icon.remove();
  });
  it("sends old addresses to the inbox and says when a project is gone", async () => {
    window.history.replaceState(null, "", "/#/decisions");
    render(<App />);
    expect(await screen.findByRole("heading", { name: "Inbox" })).toBeTruthy();
    expect(window.location.hash).toBe("#/decisions");
    go("#/projects/missing");
    expect(
      await screen.findByText(/This project isn't here any more/),
    ).toBeTruthy();
  });
  it("counts only what needs the owner, in the navigation and the title", async () => {
    state.projects = [project];
    state.decisions = [
      {
        id: "d1",
        project_id: "p1",
        title: "Which scope?",
        context: "The broader one takes longer.",
        recommendation: "Narrow",
        choices: ["Narrow", "Broad"],
        status: "open",
      },
    ];
    state.pending_operations = [{ id: "op", summary: "A result is unknown." }];
    render(<App />);
    expect(await screen.findByText("Which scope?")).toBeTruthy();
    expect(screen.getByLabelText("2 need you")).toBeTruthy();
    await waitFor(() => expect(document.title).toBe("(2) Inbox · Iris"));
    expect(screen.getByText("2 need you")).toBeTruthy();
  });
  it("opens and closes the chat with ⌘J and remembers it", async () => {
    render(<App />);
    await screen.findByLabelText("Message Iris");
    fireEvent.keyDown(document, { key: "j", metaKey: true });
    expect(screen.queryByLabelText("Message Iris")).toBeNull();
    expect(window.localStorage.getItem("crew-assistant.chat")).toBe("closed");
    fireEvent.click(screen.getByRole("button", { name: /^Chat/ }));
    expect(screen.getByLabelText("Message Iris")).toBeTruthy();
  });
  it("widens the chat without losing its draft and gives the work back", async () => {
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 0;
    });
    render(<App />);
    const field = await screen.findByLabelText("Message Iris");
    fireEvent.change(field, { target: { value: "Keep this context." } });
    fireEvent.click(screen.getByRole("button", { name: "Widen the chat" }));
    expect(
      screen
        .getByRole("button", { name: "Back to the work" })
        .getAttribute("aria-pressed"),
    ).toBe("true");
    expect(document.querySelector(".workspace")!.hasAttribute("inert")).toBe(
      true,
    );
    expect(document.activeElement).toBe(field);
    fireEvent.click(screen.getByRole("button", { name: "Back to the work" }));
    expect(document.querySelector(".workspace")!.hasAttribute("inert")).toBe(
      false,
    );
    expect(field).toHaveProperty("value", "Keep this context.");
  });
  it("on a narrow screen opens the chat as a drawer where Tab takes a suggestion", async () => {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query.includes("max-width"),
      addEventListener: () => {},
      removeEventListener: () => {},
    }));
    // jsdom lays nothing out; give controls a box so the focus trap runs.
    vi.spyOn(HTMLElement.prototype, "getClientRects").mockReturnValue([
      {},
    ] as unknown as DOMRectList);
    state.messages = [
      { id: "user-1", role: "user", content: "Plan the garden" },
      { id: "reply-1", role: "assistant", content: "Here is a plan." },
    ];
    respond = (path, options) => {
      if (path === "/api/chat/suggestion")
        return {
          body: {
            after: JSON.parse(options!.body as string).after,
            suggestion: "What should I plant first?",
          },
        };
      if (path === "/api/chat/turns") return { body: { turns: [] } };
      return { body: path === "/api/state" ? state : {} };
    };
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: /^Chat/ }));
    const drawer = await screen.findByRole("dialog", { name: "Chat" });
    const field = within(drawer).getByLabelText("Message Iris");
    await waitFor(
      () =>
        expect(field.getAttribute("placeholder")).toBe(
          "What should I plant first?",
        ),
      { timeout: 4000 },
    );
    field.focus();
    fireEvent.keyDown(field, { key: "Tab" });
    expect(field).toHaveProperty("value", "What should I plant first?");
    expect(document.activeElement).toBe(field);
    expect(calls.some((c) => c.path === "/api/chat/messages")).toBe(false);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Chat" })).toBeNull();
  });
  it("keeps a message that wasn't confirmed, ready to retry", async () => {
    respond = (path) =>
      path === "/api/chat/messages"
        ? {
            status: 503,
            body: { error: "Configure an assistant model in Settings." },
          }
        : { body: state };
    render(<App />);
    const field = await screen.findByLabelText("Message Iris");
    fireEvent.change(field, {
      target: { value: "Please coordinate this project." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByRole("alert")).toHaveProperty(
      "textContent",
      "Configure an assistant model in Settings.",
    );
    expect(screen.getByText("Please coordinate this project.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
    expect(field).toHaveProperty("value", "");
    expect(screen.queryByText("Iris is working")).toBeNull();
  });
  it("says so when it is a demo", async () => {
    state.demo = true;
    render(<App />);
    expect(await screen.findByText(/^Demo mode: no models run/)).toBeTruthy();
  });
});

describe("decisions in the inbox", () => {
  const decision = {
    id: "decision-stale",
    title: "Old setup question",
    context: "The old setup blocker may be stale.",
    recommendation: "Set up",
    choices: ["Set up", "Wait"],
    status: "open",
  };
  beforeEach(() => {
    state.projects = [project];
  });
  it("keeps a decision that failed to record, with the reason", async () => {
    state.decisions = [
      {
        ...decision,
        choices: ["Focused review", "Full review"],
        title: "Which review scope?",
      },
    ];
    respond = (path) =>
      path.includes("/resolve")
        ? {
            status: 409,
            body: {
              error: "This decision changed.",
              hint: "Refresh its context before choosing.",
            },
          }
        : { body: state };
    render(<App />);
    const choice = await screen.findByRole("button", {
      name: "Focused review",
    });
    fireEvent.click(choice);
    expect(await screen.findByRole("alert")).toHaveProperty(
      "textContent",
      "This decision changed. Refresh its context before choosing.",
    );
    expect(
      screen.getByRole("heading", { name: "Which review scope?" }),
    ).toBeTruthy();
    await waitFor(() => expect(choice).toHaveProperty("disabled", false));
    const submitted = calls.filter((c) => c.path.endsWith("/resolve"));
    expect(submitted).toHaveLength(1);
    expect(JSON.parse(submitted[0].options!.body as string)).toEqual({
      choice: "Focused review",
    });
    expect(submitted[0].options!.headers).toHaveProperty(
      "X-Requested-With",
      "crew-assistant",
    );
    expect(submitted[0].options!.credentials).toBe("same-origin");
  });
  it("records an answer in the owner's words, keeping the draft if it fails", async () => {
    state.decisions = [decision];
    let failed = true;
    respond = (path) => {
      if (path.endsWith("/resolve")) {
        if (failed)
          return { status: 400, body: { error: "Please clarify the answer." } };
        state = {
          ...state,
          decisions: [
            {
              ...decision,
              status: "resolved",
              disposition: "custom",
              answer: "Use the existing setup",
            },
          ],
        };
        return { body: state.decisions[0] };
      }
      return { body: state };
    };
    render(<App />);
    fireEvent.click(
      await screen.findByRole("button", { name: "Answer in your own words" }),
    );
    const draft = screen.getByLabelText("Your answer");
    fireEvent.change(draft, { target: { value: "Use the existing setup" } });
    fireEvent.click(screen.getByRole("button", { name: "Send answer" }));
    expect(await screen.findByRole("alert")).toHaveProperty(
      "textContent",
      "Please clarify the answer.",
    );
    expect(draft).toHaveProperty("value", "Use the existing setup");
    failed = false;
    fireEvent.click(screen.getByRole("button", { name: "Send answer" }));
    expect(
      await screen.findByText("You chose: Use the existing setup"),
    ).toBeTruthy();
    expect(writes().map((c) => JSON.parse(c.options!.body as string))).toEqual([
      { answer: "Use the existing setup" },
      { answer: "Use the existing setup" },
    ]);
  });
  it("closes a decision without deciding only with a reason", async () => {
    state.decisions = [decision];
    respond = (path) => {
      if (path.endsWith("/dismiss")) {
        state = {
          ...state,
          decisions: [
            {
              ...decision,
              status: "dismissed",
              disposition: "dismissed",
              resolution_reason: "Already configured",
            },
          ],
        };
        return { body: state.decisions[0] };
      }
      return { body: state };
    };
    render(<App />);
    fireEvent.click(
      await screen.findByRole("button", { name: "Close without deciding" }),
    );
    expect(screen.getByRole("button", { name: "Close it" })).toHaveProperty(
      "disabled",
      true,
    );
    fireEvent.change(screen.getByLabelText("Why close it without deciding?"), {
      target: { value: "Already configured" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Close it" }));
    expect(
      await screen.findByText("Closed without deciding: Already configured"),
    ).toBeTruthy();
    expect(writes()).toHaveLength(1);
    expect(writes()[0].path).toBe("/api/decisions/decision-stale/dismiss");
    expect(JSON.parse(writes()[0].options!.body as string)).toEqual({
      reason: "Already configured",
    });
  });
  it("asks what was found after an interruption, and never retries the work", async () => {
    state.pending_operations = [
      { id: "uncertain-operation", summary: "A result is unknown." },
    ];
    let fail = true;
    respond = (path) => {
      if (path.endsWith("/acknowledge")) {
        if (fail)
          return {
            status: 409,
            body: { error: "The note could not be saved." },
          };
        state = { ...state, pending_operations: [] };
        return { body: {} };
      }
      return { body: state };
    };
    render(<App />);
    expect(
      await screen.findByRole("button", { name: "Save note" }),
    ).toHaveProperty("disabled", true);
    const note = screen.getByLabelText("What did you find?");
    fireEvent.change(note, {
      target: { value: "Checked the logs: it was not accepted." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));
    expect(await screen.findByRole("alert")).toHaveProperty(
      "textContent",
      "The note could not be saved.",
    );
    expect(note).toHaveProperty(
      "value",
      "Checked the logs: it was not accepted.",
    );
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Save note" })).toBeNull(),
    );
    expect(writes()).toHaveLength(2);
    expect(
      writes().every(
        (c) => c.path === "/api/operations/uncertain-operation/acknowledge",
      ),
    ).toBe(true);
  });
});

describe("settings", () => {
  it("saves a name change with the rest of the configuration untouched", async () => {
    const config = {
      assistant: { ...state.assistant, theme: "system" },
      dashboard: { addr: "127.0.0.1:8340" },
      model: { model: "configured-model", api_key_env: "TEST_MODEL_KEY" },
    };
    respond = (path) => ({ body: path === "/api/config" ? config : state });
    window.history.replaceState(null, "", "/#/settings");
    render(<App />);
    const name = await screen.findByLabelText("Name");
    expect(
      screen.queryByRole("region", { name: "Unsaved changes" }),
    ).toBeNull();
    fireEvent.change(name, { target: { value: "Fern" } });
    fireEvent.click(
      within(screen.getByRole("region", { name: "Unsaved changes" })).getByRole(
        "button",
        { name: "Save" },
      ),
    );
    await waitFor(() =>
      expect(writes().some((c) => c.path === "/api/config")).toBe(true),
    );
    expect(
      JSON.parse(
        writes().find((c) => c.path === "/api/config")!.options!.body as string,
      ),
    ).toEqual({
      ...config,
      assistant: { ...config.assistant, name: "Fern" },
    });
    await waitFor(() =>
      expect(
        screen.queryByRole("region", { name: "Unsaved changes" }),
      ).toBeNull(),
    );
  });
  it("saves the model without touching credentials or settings it doesn't show", async () => {
    const model = {
      engine: "codex",
      model: "gpt-6-astra",
      effort: "high",
      codex_bin: "codex",
      codex_home: "/fixture/assistant-login",
      base_url: "https://api.example.test/v1",
      api_key_env: "PA_KEY",
      max_tokens: 4096,
    };
    const config = {
      assistant: state.assistant,
      model,
      worker_model: { ...model, api_key_env: "WORKER_KEY" },
    };
    respond = (path) => ({ body: path === "/api/config" ? config : state });
    window.history.replaceState(null, "", "/#/settings/model");
    render(<App />);
    expect(await screen.findByLabelText("Runs on")).toHaveProperty(
      "value",
      "codex",
    );
    expect(screen.getByLabelText("Model ID")).toHaveProperty(
      "value",
      "gpt-6-astra",
    );
    expect(screen.queryByLabelText("Most output tokens per call")).toBeNull();
    fireEvent.change(screen.getByLabelText(/^Codex folder/), {
      target: { value: "/fixture/other-login" },
    });
    fireEvent.click(
      within(screen.getByRole("region", { name: "Unsaved changes" })).getByRole(
        "button",
        { name: "Save" },
      ),
    );
    await waitFor(() =>
      expect(writes().some((c) => c.path === "/api/config")).toBe(true),
    );
    const saved = JSON.parse(
      writes().find((c) => c.path === "/api/config")!.options!.body as string,
    );
    expect(saved.model).toEqual({
      ...model,
      codex_home: "/fixture/other-login",
    });
    expect(saved.worker_model).toEqual(config.worker_model);
  });
  it("applies and keeps the appearance at once, without other unsaved edits", async () => {
    const config = {
      assistant: { ...state.assistant, theme: "system" },
      model: { engine: "openai-compatible" },
    };
    respond = (path) => ({ body: path === "/api/config" ? config : state });
    window.history.replaceState(null, "", "/#/settings/assistant");
    render(<App />);
    fireEvent.change(await screen.findByLabelText("Name"), {
      target: { value: "Unsaved" },
    });
    go("#/settings/appearance");
    fireEvent.click(await screen.findByRole("button", { name: "Dark" }));
    await waitFor(() =>
      expect(writes().some((c) => c.path === "/api/config")).toBe(true),
    );
    const saved = JSON.parse(writes()[0].options!.body as string);
    expect(saved.assistant).toEqual({ ...config.assistant, theme: "dark" });
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(window.localStorage.getItem("crew-assistant.appearance")).toBe(
      "dark",
    );
    expect(
      screen.getByRole("region", { name: "Unsaved changes" }),
    ).toBeTruthy();
  });
  it("follows the saved appearance when the dashboard loads", async () => {
    state.assistant = { ...state.assistant, theme: "light" };
    render(<App />);
    await screen.findByRole("heading", { name: "Inbox" });
    expect(document.documentElement.dataset.theme).toBe("light");
  });
  it("tells Slack reading apart from the Slack bot", async () => {
    state.integrations = [
      {
        id: "slack",
        name: "Slack bot messaging",
        status: "not_configured",
        detail: "Sends and receives owner direct messages.",
      },
      {
        id: "connection:slack-read",
        name: "Slack",
        status: "configured",
        detail: "Reading only, through CLI accounts: personal",
      },
    ];
    respond = (path) => ({
      body:
        path === "/api/config"
          ? { assistant: state.assistant, connections: [] }
          : state,
    });
    window.history.replaceState(null, "", "/#/settings/connections");
    render(<App />);
    expect(await screen.findByText("Slack bot messaging")).toBeTruthy();
    expect(
      screen.getByText(/Reading only, through CLI accounts: personal/),
    ).toBeTruthy();
  });
});

describe("memory", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/#/memory");
  });
  it("keeps preferences apart from observations and corrects rather than rewrites", async () => {
    state.memories = [
      {
        id: "mem-pref",
        content: "Always use Opus for the project worker.",
        kind: "preference",
        source: "owner",
        updated_at: "2026-09-15T10:00:00Z",
      },
      {
        id: "mem-obs",
        content: "Worker model information is unavailable.",
        kind: "observation",
        source: "assistant",
        updated_at: "2026-09-16T10:00:00Z",
        superseded_at: "0001-01-01T00:00:00Z",
      },
    ];
    render(<App />);
    const preferences = await screen.findByRole("region", {
      name: "Your preferences",
    });
    expect(within(preferences).getByText(/From you/)).toBeTruthy();
    const observations = screen.getByRole("region", { name: "Observations" });
    expect(within(observations).getByText(/From the assistant/)).toBeTruthy();
    fireEvent.click(
      within(observations).getByRole("button", { name: "Correct" }),
    );
    const field = screen.getByLabelText(/^What should it say\?/);
    expect(field).toHaveProperty(
      "value",
      "Worker model information is unavailable.",
    );
    fireEvent.change(field, {
      target: { value: "The project worker runs Opus 5." },
    });
    fireEvent.click(within(observations).getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(
        calls.some((c) => c.path === "/api/memories/mem-obs/correct"),
      ).toBe(true),
    );
    const correction = calls.find(
      (c) => c.path === "/api/memories/mem-obs/correct",
    )!;
    expect(JSON.parse(String(correction.options?.body))).toEqual({
      content: "The project worker runs Opus 5.",
    });
    expect(calls.some((c) => c.options?.method === "DELETE")).toBe(false);
  });
  it("records which kind of memory the owner chose", async () => {
    render(<App />);
    fireEvent.change(await screen.findByLabelText("Something to remember"), {
      target: { value: "Bring a recommendation with each decision." },
    });
    fireEvent.change(screen.getByLabelText("Kind"), {
      target: { value: "observation" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Remember" }));
    await waitFor(() =>
      expect(calls.some((c) => c.path === "/api/memories")).toBe(true),
    );
    expect(
      JSON.parse(
        String(calls.find((c) => c.path === "/api/memories")!.options?.body),
      ),
    ).toEqual({
      content: "Bring a recommendation with each decision.",
      kind: "observation",
    });
  });
});

describe("pairing", () => {
  it("needs a paired browser before showing anything private", async () => {
    respond = () => ({
      status: 401,
      body: { error: "This browser isn't paired." },
    });
    render(<App />);
    const input = await screen.findByLabelText(/^Pairing code/);
    expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();
    expect(
      screen.getByText("crew-assistant dashboard open --print"),
    ).toBeTruthy();
    expect(
      screen.getByText(/works once and expires after five minutes/),
    ).toBeTruthy();
    expect(input).toHaveProperty("type", "password");
    expect(input).toHaveProperty("value", "");
  });
  it("removes a pairing token before exchanging it and reuses one request", async () => {
    window.history.replaceState(null, "", "/#token=one-use-fixture");
    const first = bootstrapSession();
    const second = bootstrapSession();
    expect(window.location.hash).toBe("");
    expect(first).toBe(second);
    await first;
    expect(calls.filter((c) => c.path === "/api/session")).toHaveLength(1);
  });
});

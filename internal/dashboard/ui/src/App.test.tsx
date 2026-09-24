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
import {
  bootstrapSession,
  normalizeState,
  type Member,
  type State,
} from "./api";

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
        blob: async () => result.body,
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
    for (const name of ["Inbox", "Projects", "Team", "Memory", "Settings"])
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
  it("redraws the assistant with the look the owner gives", async () => {
    state.assistant.avatar = {
      image: "0123456789abcdef0123456789abcdef",
      look: "Short silver hair",
    };
    respond = (path) => ({
      body:
        path === "/api/config"
          ? { assistant: state.assistant }
          : path === "/api/assistant/avatar"
            ? { drawing: true }
            : state,
    });
    window.history.replaceState(null, "", "/#/settings");
    render(<App />);
    const panel = await screen.findByRole("region", { name: "Assistant" });
    expect(
      [...panel.querySelectorAll("img")].map((i) => i.getAttribute("width")),
    ).toContain("96");
    const look = within(panel).getByLabelText(/^Look/);
    expect(look).toHaveProperty("value", "Short silver hair");
    expect(
      within(panel).getByText("Leave it as it is to redraw the same look."),
    ).toBeTruthy();
    fireEvent.change(look, {
      target: { value: " Short silver hair, round glasses " },
    });
    state.assistant = { ...state.assistant, drawing: true };
    fireEvent.click(within(panel).getByRole("button", { name: "Redraw" }));
    expect(
      await within(panel).findByText("Drawing… this takes a few minutes."),
    ).toBeTruthy();
    expect(within(panel).queryByRole("button", { name: "Redraw" })).toBeNull();
    const redraw = writes().find((c) => c.path === "/api/assistant/avatar")!;
    expect(redraw.options?.method).toBe("POST");
    expect(JSON.parse(String(redraw.options?.body))).toEqual({
      look: "Short silver hair, round glasses",
    });
    expect(writes().some((c) => c.path === "/api/config")).toBe(false);
  });
  it("shows why the last drawing failed, and why a redraw was refused", async () => {
    state.assistant.draw_error = "Codex didn't save a picture";
    respond = (path) =>
      path === "/api/assistant/avatar"
        ? {
            status: 400,
            body: { error: "demo mode doesn't draw; the pictures are fixed" },
          }
        : {
            body:
              path === "/api/config" ? { assistant: state.assistant } : state,
          };
    window.history.replaceState(null, "", "/#/settings");
    render(<App />);
    const panel = await screen.findByRole("region", { name: "Assistant" });
    expect(within(panel).getByRole("alert").textContent).toBe(
      "Codex didn't save a picture",
    );
    fireEvent.click(within(panel).getByRole("button", { name: "Redraw" }));
    await waitFor(() =>
      expect(
        within(panel)
          .getAllByRole("alert")
          .map((a) => a.textContent),
      ).toContain("demo mode doesn't draw; the pictures are fixed"),
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
    await waitFor(() =>
      expect(document.documentElement.dataset.theme).toBe("light"),
    );
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

describe("the team", () => {
  const ada = (): Member => ({
    id: "m1",
    name: "Ada",
    kind: "implementer",
    engine: "claude",
    model: "opus",
    avatar_svg: '<svg xmlns="http://www.w3.org/2000/svg"/>',
    learnings: [
      { id: "l1", text: "Run the linter first.", at: "2026-09-20T10:00:00Z" },
      {
        id: "l2",
        text: "Keep commits small.",
        project_id: "p1",
        at: "2026-09-22T10:00:00Z",
      },
    ],
  });
  const staffed = (id: string, status = "active") => ({
    ...project,
    id,
    status,
    playbook: {
      template: "code",
      medium: "git",
      max_rounds: 3,
      deliver: "",
      roles: [
        { name: "Ada", kind: "implementer", engine: "claude", member: "m1" },
      ],
    },
  });
  it("says what members are for when there are none, and offers one", async () => {
    window.history.replaceState(null, "", "/#/team");
    render(<App />);
    expect(await screen.findByRole("heading", { name: "Team" })).toBeTruthy();
    expect(
      screen.getByText(
        "Members you set up here can join any project's team and keep what they learn.",
      ),
    ).toBeTruthy();
    expect(screen.getAllByRole("button", { name: "New member" })).toHaveLength(
      1,
    );
    await waitFor(() => expect(document.title).toBe("Team · Iris"));
  });
  it("lists each member with what it runs on and how much it is used", async () => {
    state.members = [
      ada(),
      {
        ...ada(),
        id: "m2",
        name: "Rune",
        kind: "qa",
        model: "",
        learnings: [],
      },
    ];
    state.projects = [staffed("p1"), staffed("p2"), staffed("p3", "completed")];
    window.history.replaceState(null, "", "/#/team");
    render(<App />);
    const card = await screen.findByRole("link", { name: /^Ada/ });
    expect(card.getAttribute("href")).toBe("#/team/m1");
    expect(card.querySelector("img")?.getAttribute("width")).toBe("40");
    expect(within(card).getByText("Implementer · Claude opus")).toBeTruthy();
    expect(within(card).getByText("In 2 projects · 2 learnings")).toBeTruthy();
    const rune = screen.getByRole("link", { name: /^Rune/ });
    expect(within(rune).getByText("QA · Claude")).toBeTruthy();
    expect(rune.textContent).not.toMatch(/project|learning/);
  });
  it("creates a member and opens it", async () => {
    window.history.replaceState(null, "", "/#/team");
    respond = (path) => ({
      body: path === "/api/members" ? { ...ada(), learnings: [] } : state,
    });
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "New member" }));
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: " Ada " },
    });
    fireEvent.change(screen.getByLabelText("Kind"), {
      target: { value: "reviewer" },
    });
    fireEvent.change(screen.getByLabelText("Engine"), {
      target: { value: "codex" },
    });
    fireEvent.change(screen.getByLabelText(/^Model/), {
      target: { value: "gpt-6" },
    });
    fireEvent.change(screen.getByLabelText(/^Instructions/), {
      target: { value: "Check the tests first." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add member" }));
    await waitFor(() => expect(window.location.hash).toBe("#/team/m1"));
    const create = writes().find((c) => c.path === "/api/members")!;
    expect(create.options?.method).toBe("POST");
    expect(JSON.parse(String(create.options?.body))).toEqual({
      name: "Ada",
      kind: "reviewer",
      engine: "codex",
      model: "gpt-6",
      effort: "",
      instructions: "Check the tests first.",
    });
  });
  it("shows a refused save in the form", async () => {
    window.history.replaceState(null, "", "/#/team");
    respond = (path) =>
      path === "/api/members"
        ? {
            status: 400,
            body: { error: "There is already a member called Ada" },
          }
        : { body: state };
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "New member" }));
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "ada" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add member" }));
    expect((await screen.findByRole("alert")).textContent).toBe(
      "There is already a member called Ada",
    );
    expect(window.location.hash).toBe("#/team");
  });
  it("says a member is being drawn in place of how much it is used", async () => {
    state.members = [{ ...ada(), drawing: true }];
    state.projects = [staffed("p1")];
    window.history.replaceState(null, "", "/#/team");
    render(<App />);
    const card = await screen.findByRole("link", { name: /^Ada/ });
    expect(within(card).getByText("Drawing…")).toBeTruthy();
    expect(card.textContent).not.toMatch(/project|learning/);
  });
  it("redraws a member with a new look, then says it is drawing", async () => {
    state.members = [
      { ...ada(), avatar: { shape: "orb", look: "Curly red hair" } },
    ];
    respond = (path) => ({
      body: path === "/api/members/m1/avatar" ? { drawing: true } : state,
    });
    window.history.replaceState(null, "", "/#/team/m1");
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Redraw" }));
    const look = screen.getByLabelText(/^Look/);
    expect(look).toHaveProperty("value", "Curly red hair");
    fireEvent.change(look, { target: { value: "Curly red hair, freckles" } });
    state.members = [{ ...state.members[0], drawing: true }];
    fireEvent.click(screen.getByRole("button", { name: "Redraw" }));
    expect(
      await screen.findByText("Drawing… this takes a few minutes."),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Redraw" })).toBeNull();
    expect(screen.queryByLabelText(/^Look/)).toBeNull();
    const redraw = writes().find((c) => c.path === "/api/members/m1/avatar")!;
    expect(redraw.options?.method).toBe("POST");
    expect(JSON.parse(String(redraw.options?.body))).toEqual({
      look: "Curly red hair, freckles",
    });
  });
  it("shows why a member's drawing failed", async () => {
    state.members = [{ ...ada(), draw_error: "Codex didn't save a picture" }];
    window.history.replaceState(null, "", "/#/team/m1");
    render(<App />);
    expect((await screen.findByRole("alert")).textContent).toBe(
      "Codex didn't save a picture",
    );
    expect(screen.getByRole("button", { name: "Redraw" })).toBeTruthy();
  });
  it("shows a member's projects and learnings, newest first, and adds and forgets them", async () => {
    state.members = [ada()];
    state.projects = [staffed("p1"), staffed("p2", "completed")];
    window.history.replaceState(null, "", "/#/team/m1");
    render(<App />);
    expect(await screen.findByRole("heading", { name: "Ada" })).toBeTruthy();
    const projects = screen.getByRole("region", { name: "Projects" });
    expect(
      within(projects)
        .getAllByRole("link")
        .map((a) => a.getAttribute("href")),
    ).toEqual(["#/projects/p1/team"]);
    const learnings = screen.getByRole("region", { name: "Learnings" });
    expect(
      within(learnings).getByText(
        "Ada starts each task knowing when each one applies, and reads it only then. It adds its own too, never about one project.",
      ),
    ).toBeTruthy();
    const rows = within(learnings).getAllByRole("listitem");
    expect(rows.map((r) => r.querySelector("p")?.textContent)).toEqual([
      "Keep commits small.",
      "Run the linter first.",
    ]);
    expect(rows[0].textContent).toContain("You added this on Launch note · ");
    fireEvent.change(within(learnings).getByLabelText(/^When it applies/), {
      target: { value: " Finishing a change " },
    });
    fireEvent.change(within(learnings).getByLabelText("Learning"), {
      target: { value: " Say which tests ran. " },
    });
    fireEvent.change(within(learnings).getByLabelText("Learned on"), {
      target: { value: "p1" },
    });
    fireEvent.click(within(learnings).getByRole("button", { name: "Add" }));
    await waitFor(() =>
      expect(writes().some((c) => c.path === "/api/members/m1/learnings")).toBe(
        true,
      ),
    );
    const add = writes().find((c) => c.path === "/api/members/m1/learnings")!;
    expect(add.options?.method).toBe("POST");
    expect(JSON.parse(String(add.options?.body))).toEqual({
      when: "Finishing a change",
      text: "Say which tests ran.",
      project_id: "p1",
    });
    await waitFor(() =>
      expect(within(learnings).getByLabelText("Learning")).toHaveProperty(
        "value",
        "",
      ),
    );
    expect(within(learnings).getByLabelText(/^When it applies/)).toHaveProperty(
      "value",
      "",
    );
    fireEvent.click(within(rows[1]).getByRole("button", { name: "Forget" }));
    await waitFor(() =>
      expect(
        writes().some((c) => c.path === "/api/members/m1/learnings/l1"),
      ).toBe(true),
    );
    expect(
      writes().find((c) => c.path === "/api/members/m1/learnings/l1")!.options
        ?.method,
    ).toBe("DELETE");
  });
  it("reads each learning like a skill: when it applies, what to do, and who learned it", async () => {
    const long = `Check that every error is wrapped with what was being done. ${"Name the operation and the input that failed. ".repeat(6)}`;
    state.members = [
      {
        ...ada(),
        learnings: [
          {
            id: "l1",
            when: "Reviewing error handling",
            text: long,
            source: "member",
            project_id: "p1",
            at: "2026-09-22T10:00:00Z",
          },
          {
            id: "l2",
            text: "Keep commits small. One idea each.",
            source: "assistant",
            at: "2026-09-21T10:00:00Z",
          },
        ],
      },
    ];
    state.projects = [staffed("p1")];
    window.history.replaceState(null, "", "/#/team/m1");
    render(<App />);
    const learnings = await screen.findByRole("region", { name: "Learnings" });
    const rows = within(learnings).getAllByRole("listitem");
    const lines = (row: HTMLElement) =>
      [...row.querySelectorAll("p")].map((p) => p.textContent);
    expect(lines(rows[0])[0]).toBe("Reviewing error handling");
    expect(lines(rows[0])[1]).toMatch(/^Check that every error is wrapped.*…/);
    expect(lines(rows[0])[1]).not.toContain(long.trim());
    expect(lines(rows[0])[2]).toMatch(/^Ada learned this on Launch note · /);
    fireEvent.click(within(rows[0]).getByRole("button", { name: "Show all" }));
    expect(lines(rows[0])[1]).toBe(long.trim());
    expect(lines(rows[1])).toEqual([
      "Keep commits small.",
      "One idea each.",
      expect.stringMatching(/^Added by Iris · /),
    ]);
    const when = within(learnings).getByLabelText(/^When it applies/);
    expect(when.getAttribute("maxlength")).toBe("160");
    expect(when.getAttribute("placeholder")).toBe(
      "e.g. Reviewing error handling",
    );
    expect(
      within(learnings).getByLabelText("Learning").getAttribute("maxlength"),
    ).toBe("1500");
  });
  it("shows why a learning was refused", async () => {
    state.members = [ada()];
    respond = (path) =>
      path === "/api/members/m1/learnings"
        ? {
            status: 409,
            body: { error: "Ada already keeps 30 learnings; forget one first" },
          }
        : { body: state };
    window.history.replaceState(null, "", "/#/team/m1");
    render(<App />);
    fireEvent.change(await screen.findByLabelText("Learning"), {
      target: { value: "One more." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect((await screen.findByRole("alert")).textContent).toBe(
      "Ada already keeps 30 learnings; forget one first",
    );
    expect(screen.getByLabelText("Learning")).toHaveProperty(
      "value",
      "One more.",
    );
  });
  it("deletes a member only once the owner confirms, then goes back to the team", async () => {
    state.members = [ada()];
    window.history.replaceState(null, "", "/#/team/m1");
    render(<App />);
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete member" }),
    );
    expect(writes()).toHaveLength(0);
    expect(screen.getByText("Project teams keep their copy.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Keep" }));
    expect(screen.queryByRole("button", { name: "Delete Ada" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Delete member" }));
    state.members = [];
    fireEvent.click(screen.getByRole("button", { name: "Delete Ada" }));
    await waitFor(() => expect(window.location.hash).toBe("#/team"));
    expect(writes()).toEqual([
      expect.objectContaining({
        path: "/api/members/m1",
        options: expect.objectContaining({ method: "DELETE" }),
      }),
    ]);
  });
  it("edits a member in place", async () => {
    state.members = [ada()];
    window.history.replaceState(null, "", "/#/team/m1");
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    expect(screen.getByLabelText("Name")).toHaveProperty("value", "Ada");
    expect(screen.getByLabelText(/^Model/)).toHaveProperty("value", "opus");
    fireEvent.change(screen.getByLabelText(/^Reasoning effort/), {
      target: { value: "high" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Edit" })).toBeTruthy(),
    );
    const save = writes().find((c) => c.path === "/api/members/m1")!;
    expect(save.options?.method).toBe("PUT");
    expect(JSON.parse(String(save.options?.body))).toMatchObject({
      name: "Ada",
      kind: "implementer",
      engine: "claude",
      model: "opus",
      effort: "high",
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

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

describe("owner dashboard flows", () => {
  it("uses the configured name and an honest outcome-focused empty workspace", async () => {
    render(<App />);
    expect(
      await screen.findByRole("button", { name: "Iris overview" }),
    ).toBeTruthy();
    expect(
      screen.getByRole("heading", { name: "Start with the outcome" }),
    ).toBeTruthy();
    expect(screen.getByRole("button", { name: "Overview" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Today" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Tomorrow" })).toBeNull();
    expect(screen.getByLabelText("Message Iris")).toBeTruthy();
    expect(screen.queryByText("Preview mode", { exact: false })).toBeNull();
  });
  it("expands the conversation without losing its draft and restores navigation", async () => {
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 0;
    });
    render(<App />);
    const field = await screen.findByLabelText("Message Iris");
    fireEvent.change(field, { target: { value: "Keep this context." } });
    fireEvent.click(
      screen.getByRole("button", { name: "Expand conversation" }),
    );
    expect(
      screen
        .getByRole("button", { name: "Return to workspace" })
        .getAttribute("aria-pressed"),
    ).toBe("true");
    expect(document.querySelector(".workspace")!.hasAttribute("inert")).toBe(
      true,
    );
    expect(document.activeElement).toBe(field);
    fireEvent.click(
      screen.getByRole("button", { name: "Return to workspace" }),
    );
    expect(document.querySelector(".workspace")!.hasAttribute("inert")).toBe(
      false,
    );
    expect(field).toHaveProperty("value", "Keep this context.");
  });
  it("accepts a suggestion with Tab inside the mobile conversation without sending or moving focus", async () => {
    // jsdom lays nothing out; give controls a box so the focus trap runs and
    // sees the empty draft as the last enabled control, as a browser does.
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
    fireEvent.click(
      await screen.findByRole("button", { name: "Open conversation" }),
    );
    const field = screen.getByLabelText("Message Iris") as HTMLTextAreaElement;
    field.focus();
    await waitFor(
      () => expect(field.placeholder).toBe("What should I plant first?"),
      { timeout: 3000 },
    );
    fireEvent.keyDown(field, { key: "Tab" });
    expect(field.value).toBe("What should I plant first?");
    expect(document.activeElement).toBe(field);
    expect(
      calls.filter(
        (c) => c.path === "/api/chat/messages" && c.options?.method === "POST",
      ),
    ).toHaveLength(0);
    // Plain Tab from the last control still wraps focus within the dialog.
    fireEvent.change(field, { target: { value: "" } });
    fireEvent.keyDown(field, { key: "Tab" });
    expect(document.activeElement).not.toBe(field);
    vi.restoreAllMocks();
  });
  it("keeps a failed decision visible and exposes the actionable error", async () => {
    state.decisions = [
      {
        id: "decision-1",
        title: "Which review scope?",
        context: "The broader review takes longer.",
        recommendation: "Review the changed behavior first.",
        choices: ["Focused review", "Full review"],
        status: "pending",
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
  it("preserves the submitted message when delivery is not confirmed", async () => {
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
    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    expect(await screen.findByRole("alert")).toHaveProperty(
      "textContent",
      "Configure an assistant model in Settings.",
    );
    expect(screen.getByText("Please coordinate this project.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry delivery" })).toBeTruthy();
    expect(field).toHaveProperty("value", "");
    expect(screen.queryByText("Iris is working through it…")).toBeNull();
  });
  it("creates a written-work project with its brief without starting work", async () => {
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Add project" }));
    const dialog = screen.getByRole("dialog");
    expect(
      within(dialog)
        .getByRole("button", { name: "Written work (writer + reviewer)" })
        .getAttribute("aria-pressed"),
    ).toBe("true");
    fireEvent.change(screen.getByLabelText("Project name"), {
      target: { value: "Launch note" },
    });
    expect(
      screen.getByRole("button", { name: "Create project" }),
    ).toHaveProperty("disabled", true);
    fireEvent.change(screen.getByLabelText("Goal"), {
      target: { value: "Tell customers what changed." },
    });
    fireEvent.change(screen.getByLabelText("Audience"), {
      target: { value: "Existing customers" },
    });
    fireEvent.change(screen.getByLabelText("What does done look like?"), {
      target: { value: "Under 300 words\n\n Links to the changelog " },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    await waitFor(() =>
      expect(calls.some((c) => c.path === "/api/projects")).toBe(true),
    );
    const body = JSON.parse(
      calls.find((c) => c.path === "/api/projects")!.options!.body as string,
    );
    expect(body).toEqual({
      title: "Launch note",
      directories: [],
      brief: {
        goal: "Tell customers what changed.",
        audience: "Existing customers",
        constraints: "",
        criteria: ["Under 300 words", "Links to the changelog"],
      },
      template: "draft",
    });
    expect(calls.some((c) => c.path.endsWith("/tasks"))).toBe(false);
  });
  it("preserves unrelated configuration when changing the assistant name", async () => {
    const config = {
      assistant: state.assistant,
      dashboard: { addr: "127.0.0.1:8340" },
      workers: [
        {
          id: "runner-a",
          name: "Local worker",
          endpoint: "http://127.0.0.1:8350",
          api_key_env: "WORKER_KEY",
          capabilities: ["implement"],
        },
      ],
      model: { model: "configured-model", api_key_env: "TEST_MODEL_KEY" },
    };
    respond = (path) => ({ body: path === "/api/config" ? config : state });
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: /^Settings$/ }));
    const name = await screen.findByLabelText("Name");
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Save preferences" }),
      ).toHaveProperty("disabled", false),
    );
    fireEvent.change(name, { target: { value: "Fern" } });
    fireEvent.click(screen.getByRole("button", { name: "Save preferences" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.path === "/api/config" && c.options?.method === "PUT",
        ),
      ).toBe(true),
    );
    const saved = JSON.parse(
      calls.find(
        (c) => c.path === "/api/config" && c.options?.method === "PUT",
      )!.options!.body as string,
    );
    expect(saved).toEqual({
      ...config,
      assistant: { ...config.assistant, name: "Fern" },
    });
  });
  it("saves the assistant model without changing provider credentials or other profiles", async () => {
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
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: /^Settings$/ }));
    expect(await screen.findByLabelText("Assistant engine")).toHaveProperty(
      "value",
      "codex",
    );
    expect(
      screen.getByLabelText(/^Assistant custom model identifier/),
    ).toHaveProperty("value", "gpt-6-astra");
    expect(screen.getByLabelText(/^Assistant reasoning effort/)).toHaveProperty(
      "value",
      "high",
    );
    expect(
      screen.queryByLabelText(/^Assistant maximum output tokens per call/),
    ).toBeNull();
    expect(screen.getByLabelText(/^Assistant Codex home/)).toHaveProperty(
      "value",
      "/fixture/assistant-login",
    );
    fireEvent.change(screen.getByLabelText(/^Assistant Codex home/), {
      target: { value: "/fixture/other-login" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save preferences" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.path === "/api/config" && c.options?.method === "PUT",
        ),
      ).toBe(true),
    );
    const saved = JSON.parse(
      calls.find(
        (c) => c.path === "/api/config" && c.options?.method === "PUT",
      )!.options!.body as string,
    );
    expect(saved.model).toEqual({
      ...model,
      codex_home: "/fixture/other-login",
    });
    // Configuration the dashboard no longer edits is saved back untouched.
    expect(saved.worker_model).toEqual(config.worker_model);
  });
  it("requires an inspection note, preserves it after failure, and never retries interrupted work", async () => {
    state.pending_operations = [
      {
        id: "uncertain-operation",
        summary: "A dispatch result is unknown.",
      },
    ];
    let fail = true;
    respond = (path) => {
      if (path.endsWith("/acknowledge")) {
        if (fail)
          return {
            status: 409,
            body: { error: "Inspection could not be recorded." },
          };
        state = { ...state, pending_operations: [] };
        return { body: {} };
      }
      return { body: state };
    };
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: /^Decisions/ }));
    expect(
      await screen.findByRole("button", { name: "Record inspection" }),
    ).toHaveProperty("disabled", true);
    expect(screen.queryByText("Nothing needs your decision")).toBeNull();
    const note = screen.getByLabelText("What did you find?");
    fireEvent.change(note, {
      target: { value: "Checked broker logs: dispatch was not accepted." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Record inspection" }));
    expect(await screen.findByRole("alert")).toHaveProperty(
      "textContent",
      "Inspection could not be recorded.",
    );
    expect(note).toHaveProperty(
      "value",
      "Checked broker logs: dispatch was not accepted.",
    );
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Record inspection" }));
    expect(await screen.findByText("Nothing needs your decision")).toBeTruthy();
    const writes = calls.filter((c) => c.options?.method === "POST");
    expect(writes).toHaveLength(2);
    expect(
      writes.every(
        (c) => c.path === "/api/operations/uncertain-operation/acknowledge",
      ),
    ).toBe(true);
    expect(JSON.parse(writes[0].options!.body as string)).toEqual({
      note: "Checked broker logs: dispatch was not accepted.",
    });
  });
  it("requires owner access before rendering private project data", async () => {
    respond = () => ({
      status: 401,
      body: { error: "Owner access required." },
    });
    render(<App />);
    expect(await screen.findByLabelText("Dashboard access code")).toBeTruthy();
    expect(
      screen.queryByRole("navigation", { name: "Main navigation" }),
    ).toBeNull();
  });
  it("explains how to obtain a sign-in code without revealing one", async () => {
    respond = () => ({
      status: 401,
      body: { error: "Owner access required." },
    });
    render(<App />);
    await screen.findByLabelText("Dashboard access code");
    expect(
      screen.getByText("crew-assistant dashboard open --print"),
    ).toBeTruthy();
    expect(screen.getByText(/computer hosting your assistant/)).toBeTruthy();
    expect(screen.getByText(/expires after five minutes/)).toBeTruthy();
    expect(screen.getByText(/works once/)).toBeTruthy();
    const input = screen.getByLabelText(
      "Dashboard access code",
    ) as HTMLInputElement;
    expect(input.type).toBe("password");
    expect(input.value).toBe("");
  });
  it("removes a pairing token before exchanging it and reuses one request", async () => {
    window.history.replaceState(null, "", "/#token=one-use-fixture");
    const first = bootstrapSession();
    const second = bootstrapSession();
    expect(window.location.hash).toBe("");
    expect(first).toBe(second);
    await first;
    expect(calls.filter((c) => c.path === "/api/session")).toHaveLength(1);
    expect(window.localStorage.length).toBe(0);
  });
});

describe("decision alternatives", () => {
  const decision = {
    id: "decision-stale",
    title: "Old setup question",
    context: "The old setup blocker may be stale.",
    recommendation: "Set up",
    choices: ["Set up", "Wait"],
    status: "open",
  };
  it("records a custom answer and preserves the draft after failure", async () => {
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
    fireEvent.click(await screen.findByRole("button", { name: /^Decisions/ }));
    fireEvent.click(
      screen.getByRole("button", { name: "Give a different answer" }),
    );
    const draft = screen.getByLabelText("Your answer");
    fireEvent.change(draft, { target: { value: "Use the existing setup" } });
    fireEvent.click(screen.getByRole("button", { name: "Record answer" }));
    expect(await screen.findByRole("alert")).toHaveProperty(
      "textContent",
      "Please clarify the answer.",
    );
    expect(draft).toHaveProperty("value", "Use the existing setup");
    failed = false;
    fireEvent.click(screen.getByRole("button", { name: "Record answer" }));
    expect(
      await screen.findByText("Answer: Use the existing setup"),
    ).toBeTruthy();
    const writes = calls.filter((c) => c.options?.method === "POST");
    expect(writes).toHaveLength(2);
    expect(JSON.parse(writes[0].options!.body as string)).toEqual({
      answer: "Use the existing setup",
    });
  });
  it("requires a reason for dismissal and does not request any controls", async () => {
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
    fireEvent.click(await screen.findByRole("button", { name: /^Decisions/ }));
    fireEvent.click(screen.getByRole("button", { name: "No longer needed" }));
    expect(
      screen.getByRole("button", { name: "Dismiss decision" }),
    ).toHaveProperty("disabled", true);
    expect(
      screen.getByText(/does not approve or restart any work/),
    ).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Why is this no longer needed?"), {
      target: { value: "Already configured" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Dismiss decision" }));
    expect(await screen.findByText("Reason: Already configured")).toBeTruthy();
    const writes = calls.filter((c) => c.options?.method === "POST");
    expect(writes).toHaveLength(1);
    expect(writes[0].path).toBe("/api/decisions/decision-stale/dismiss");
    expect(JSON.parse(writes[0].options!.body as string)).toEqual({
      reason: "Already configured",
    });
  });

  it("separates Slack reading from Slack bot messaging", async () => {
    state.integrations = [
      {
        id: "slack",
        name: "Slack bot messaging",
        status: "not_configured",
        detail:
          "Sends and receives owner direct messages. Configure owner identity and Socket Mode credentials",
      },
      {
        id: "connection:slack-read",
        name: "Slack",
        status: "configured",
        detail: "Reading only, through CLI accounts: personal",
      },
    ];
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: /^Settings$/ }));
    expect(await screen.findByText("Slack bot messaging")).toBeTruthy();
    expect(
      screen.getByText(/Reading only, through CLI accounts: personal/),
    ).toBeTruthy();
    expect(
      screen.getByText(/Sends and receives owner direct messages/),
    ).toBeTruthy();
    state.integrations = [];
  });

  it("separates standing preferences from observations and corrects rather than rewrites", async () => {
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
      },
    ];
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: /^Memory$/ }));
    await screen.findByText(/Standing preference/);
    const cards = document.querySelectorAll(".memory-card");
    expect((cards[0] as HTMLElement).textContent).toContain(
      "Standing preference · From you",
    );
    expect((cards[1] as HTMLElement).textContent).toContain(
      "Observation, true when recorded · From your assistant",
    );

    const observation = cards[1] as HTMLElement;
    fireEvent.click(
      within(observation).getByRole("button", { name: "Correct" }),
    );
    const field = screen.getByLabelText("What should it say instead?");
    expect((field as HTMLTextAreaElement).value).toBe(
      "Worker model information is unavailable.",
    );
    expect(
      screen.getByText(/The original is kept and marked corrected/),
    ).toBeTruthy();
    fireEvent.change(field, {
      target: { value: "The project worker runs Opus 5." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save correction" }));
    await waitFor(() =>
      expect(
        calls.some((c) => c.path === "/api/memories/mem-obs/correct"),
      ).toBe(true),
    );
    const correction = calls.find(
      (c) => c.path === "/api/memories/mem-obs/correct",
    )!;
    expect(correction.options?.method).toBe("POST");
    expect(JSON.parse(String(correction.options?.body))).toEqual({
      content: "The project worker runs Opus 5.",
    });
    // Correcting is not forgetting.
    expect(calls.some((c) => c.options?.method === "DELETE")).toBe(false);
    state.memories = [];
  });

  it("records which kind of memory the owner chose", async () => {
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: /^Memory$/ }));
    fireEvent.change(screen.getByLabelText("Something worth remembering"), {
      target: { value: "Bring a recommendation with each decision." },
    });
    fireEvent.change(screen.getByLabelText("What kind of thing is this?"), {
      target: { value: "observation" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Remember this" }));
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

// @vitest-environment jsdom
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { EngineSettings, hostSummary } from "./EngineSettings";
import { rememberChoices } from "./engines";
import { testChoices } from "./testEngines";
import { normalizeState, type Config } from "./api";
import { Settings } from "./SettingsPage";
import { recordFetch, reply } from "./testFetch";
import { useChatPane } from "./chatPane";

beforeEach(() => {
  rememberChoices(testChoices);
  HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  HTMLDialogElement.prototype.close = function () {
    if (!this.open) return;
    this.open = false;
    this.dispatchEvent(new Event("close"));
  };
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation(async (url: string) => ({
      ok: true,
      json: async () => ({
        available: url.includes("claude"),
        engine: "claude",
        models: [],
      }),
    })),
  );
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
const config = {
  engines: {
    claude: { bin: "/test/claude" },
    codex: { home: "/test/login", usage_floor: { "5h_percent": 20 } },
  },
} as Config;
const defaults = {
  engines: {
    claude: { bin: "claude", home: "/test/claude-default" },
    codex: { bin: "codex", home: "/test/codex-default" },
    grok: { bin: "grok", home: "" },
  },
  openai_base_url: "https://api.example.test/v1",
};
function setup(value = config, onSave = vi.fn().mockResolvedValue(undefined)) {
  render(<EngineSettings config={value} defaults={defaults} onSave={onSave} />);
  return onSave;
}
function open(name: string) {
  const button = screen.getByRole("button", { name: new RegExp(name) });
  fireEvent.click(button);
  expectFocusAfterClose(button);
  return button;
}
function expectFocusAfterClose(button: HTMLElement) {
  const focus = button.focus.bind(button);
  vi.spyOn(button, "focus").mockImplementation(() => {
    expect(document.querySelector("dialog[open]")).toBeNull();
    focus();
  });
}
function save() {
  fireEvent.click(
    within(screen.getByRole("dialog")).getByRole("button", { name: "Save" }),
  );
}
function backdrop(dialog: HTMLElement) {
  fireEvent.mouseDown(dialog, { clientX: -1, clientY: -1 });
  fireEvent.click(dialog, { clientX: -1, clientY: -1 });
}
it("shows statuses from checks and every CLI without paths", async () => {
  setup();
  expect(
    screen
      .getAllByText("Checking…")
      .every((el) => el.classList.contains("tone-work")),
  ).toBe(true);
  await screen.findByText("Connected");
  expect(screen.getByText("Connected").classList.contains("tone-done")).toBe(
    true,
  );
  expect(
    screen
      .getAllByText("Not connected")
      .every((el) => el.classList.contains("tone-wait")),
  ).toBe(true);
  expect(screen.queryByText("/test/claude")).toBeNull();
  open("Grok");
  expect(
    screen.getByLabelText<HTMLInputElement>("Grok program").placeholder,
  ).toBe("grok");
  expect(
    screen.getByLabelText<HTMLInputElement>(/^Grok folder/).placeholder,
  ).toBe("");
});
it("shows pending and failed checks without preventing editing", async () => {
  vi.mocked(fetch).mockImplementation((url) =>
    String(url).includes("claude")
      ? Promise.reject(new Error("missing"))
      : new Promise(() => {}),
  );
  setup();
  await screen.findByText("Not connected");
  expect(screen.getAllByText("Checking…")).toHaveLength(2);
  open("Claude");
  expect(screen.getByRole("dialog", { name: "Edit Claude" })).toBeTruthy();
});
it("keeps placeholders and saves just the edited CLI, dropping blanks and refreshing", async () => {
  const onSave = setup();
  open("Claude");
  expect(
    screen.getByLabelText<HTMLInputElement>("Claude program").placeholder,
  ).toBe("claude");
  expect(
    screen.getByLabelText<HTMLInputElement>(/^Claude settings folder/)
      .placeholder,
  ).toBe("/test/claude-default");
  expect(screen.getByText("Uses your existing Claude login.")).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Claude program"), {
    target: { value: "" },
  });
  save();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(onSave).toHaveBeenCalledWith({ ...config.engines, claude: {} });
  open("Codex");
  expect(screen.getByLabelText<HTMLInputElement>(/^Codex folder/).value).toBe(
    "/test/login",
  );
  fireEvent.change(screen.getByLabelText("Codex program"), {
    target: { value: "/bin/codex" },
  });
  save();
  await waitFor(() => expect(onSave).toHaveBeenCalledTimes(2));
  expect(onSave).toHaveBeenLastCalledWith({
    ...config.engines,
    codex: {
      home: "/test/login",
      usage_floor: { "5h_percent": 20 },
      bin: "/bin/codex",
    },
  });
  await waitFor(() =>
    expect(
      vi
        .mocked(fetch)
        .mock.calls.filter(([url]) => String(url).includes("engine=codex"))
        .length,
    ).toBe(2),
  );
});
for (const name of [
  "Claude",
  "OpenAI-compatible",
  "OpenRouter",
  "Add API connection",
])
  for (const method of ["Cancel", "Close", "Escape", "backdrop"])
    it(`dismisses ${name} with ${method} and returns focus without saving`, () => {
      const onSave = setup({
        ...config,
        engines: { ...config.engines, providers: [provider] },
      });
      const button = open(name);
      const dialog = screen.getByRole("dialog");
      if (method === "Escape")
        fireEvent(dialog, new Event("cancel", { cancelable: true }));
      else if (method === "backdrop") backdrop(dialog);
      else fireEvent.click(screen.getByRole("button", { name: method }));
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(onSave).not.toHaveBeenCalled();
      expect(document.activeElement).toBe(button);
    });
it("lists the default host and saves its effort and key without removal", async () => {
  const onSave = setup();
  const rows = screen.getAllByRole("listitem");
  expect(rows[0].textContent).toContain("OpenAI-compatible (default)");
  expect(rows[0].textContent).toContain("api.example.test");
  open("OpenAI-compatible");
  expect(
    screen.getByRole("dialog", { name: "Edit OpenAI-compatible API" }),
  ).toBeTruthy();
  expect(screen.queryByText("Remove connection")).toBeNull();
  expect(
    screen.getByLabelText<HTMLInputElement>("API address").placeholder,
  ).toBe(defaults.openai_base_url);
  fireEvent.change(screen.getByLabelText(/^API key variable/), {
    target: { value: "TEST_KEY" },
  });
  fireEvent.change(screen.getByLabelText(/^Reasoning effort/), {
    target: { value: "reasoning.effort" },
  });
  save();
  await waitFor(() => expect(onSave).toHaveBeenCalledOnce());
  expect(onSave).toHaveBeenCalledWith({
    ...config.engines,
    "openai-compatible": {
      api_key_env: "TEST_KEY",
      effort_parameter: "reasoning.effort",
    },
  });
});
it("adds using required fields and keeps a manually edited id", async () => {
  const onSave = setup();
  const button = open("Add API connection");
  save();
  expect(onSave).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("Name"), {
    target: { value: "Open Router" },
  });
  expect(screen.getByLabelText<HTMLInputElement>(/^Id/).value).toBe(
    "open-router",
  );
  fireEvent.change(screen.getByLabelText(/^Id/), {
    target: { value: "custom" },
  });
  fireEvent.change(screen.getByLabelText(/^Id/), {
    target: { value: "open-router" },
  });
  fireEvent.change(screen.getByLabelText("Name"), {
    target: { value: "OpenRouter" },
  });
  expect(screen.getByLabelText<HTMLInputElement>(/^Id/).value).toBe(
    "open-router",
  );
  fireEvent.change(screen.getByLabelText("API address"), {
    target: { value: "https://openrouter.ai/api/v1" },
  });
  fireEvent.change(screen.getByLabelText(/^API key variable/), {
    target: { value: "TEST_KEY" },
  });
  save();
  await waitFor(() => expect(onSave).toHaveBeenCalledOnce());
  expect(onSave).toHaveBeenCalledWith({
    ...config.engines,
    providers: [
      {
        id: "open-router",
        name: "OpenRouter",
        base_url: "https://openrouter.ai/api/v1",
        api_key_env: "TEST_KEY",
      },
    ],
  });
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(document.activeElement).toBe(button);
});
const provider = {
  id: "router",
  name: "OpenRouter",
  base_url: "https://openrouter.ai/api/v1",
  api_key_env: "SECRET_VARIABLE",
};
it("shows named hosts, confirms removal, and focuses Add", async () => {
  const onSave = setup({
    ...config,
    engines: { ...config.engines, providers: [provider] },
  });
  expect(screen.getByText("openrouter.ai")).toBeTruthy();
  expect(screen.queryByText("SECRET_VARIABLE")).toBeNull();
  expectFocusAfterClose(
    screen.getByRole("button", { name: "Add API connection" }),
  );
  open("OpenRouter");
  fireEvent.click(screen.getByText("Remove connection"));
  fireEvent.click(screen.getByText("Keep"));
  expect(onSave).not.toHaveBeenCalled();
  fireEvent.click(screen.getByText("Remove connection"));
  fireEvent.click(screen.getByText("Remove connection"));
  await waitFor(() => expect(onSave).toHaveBeenCalledWith(config.engines));
  expect(document.activeElement).toBe(
    screen.getByRole("button", { name: "Add API connection" }),
  );
});
it("keeps edits and errors in a failed save and removal", async () => {
  const onSave = setup(
    { ...config, engines: { ...config.engines, providers: [provider] } },
    vi.fn().mockRejectedValue(new Error("Provider is still used")),
  );
  open("OpenRouter");
  fireEvent.change(screen.getByLabelText("Name"), {
    target: { value: "Changed" },
  });
  save();
  await screen.findByRole("alert");
  expect(screen.getByRole("alert").textContent).toContain(
    "Provider is still used",
  );
  expect(screen.getByLabelText<HTMLInputElement>("Name").value).toBe("Changed");
  fireEvent.click(screen.getByText("Remove connection"));
  fireEvent.click(screen.getByText("Remove connection"));
  await waitFor(() => expect(onSave).toHaveBeenCalledTimes(2));
  expect(screen.getByRole("dialog")).toBeTruthy();
});
it("keeps the dialog form outside the page form", () => {
  render(
    <form aria-label="Page">
      <EngineSettings config={config} onSave={vi.fn()} />
    </form>,
  );
  open("Claude");
  expect(
    within(screen.getByRole("form", { name: "Page" })).queryByRole("dialog"),
  ).toBeNull();
});
it("saves an existing API connection's fields and drops default effort", async () => {
  const value = {
    ...config,
    engines: {
      ...config.engines,
      providers: [
        { ...provider, effort_parameter: "reasoning.effort" as const },
      ],
    },
  };
  const onSave = setup(value);
  open("OpenRouter");
  fireEvent.change(screen.getByLabelText("API address"), {
    target: { value: "http://localhost:11434/v1" },
  });
  fireEvent.change(screen.getByLabelText(/^Reasoning effort/), {
    target: { value: "" },
  });
  save();
  await waitFor(() => expect(onSave).toHaveBeenCalledOnce());
  expect(onSave).toHaveBeenCalledWith({
    ...config.engines,
    providers: [{ ...provider, base_url: "http://localhost:11434/v1" }],
  });
});
it("blocks dismissal and duplicate saves while writing", async () => {
  let finish!: () => void;
  const onSave = setup(
    config,
    vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        }),
    ),
  );
  open("Claude");
  save();
  save();
  fireEvent.click(screen.getByRole("button", { name: "Close" }));
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  fireEvent(
    screen.getByRole("dialog"),
    new Event("cancel", { cancelable: true }),
  );
  backdrop(screen.getByRole("dialog"));
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(onSave).toHaveBeenCalledOnce();
  finish();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});
it("summarizes only valid hosts, including ports", () => {
  expect(hostSummary("https://openrouter.ai/api/v1")).toBe("openrouter.ai");
  expect(hostSummary("http://localhost:11434/v1")).toBe("localhost:11434");
  expect(hostSummary("")).toBe("No API address set");
  expect(hostSummary("invalid")).toBe("No API address set");
});

for (const name of [
  "Claude",
  "OpenAI-compatible",
  "OpenRouter",
  "Add API connection",
])
  it(`keeps ${name} edits for padding clicks and drags onto the backdrop`, () => {
    const onSave = setup({
      ...config,
      engines: { ...config.engines, providers: [provider] },
    });
    open(name);
    const dialog = screen.getByRole("dialog");
    vi.spyOn(dialog, "getBoundingClientRect").mockReturnValue({
      left: 100,
      right: 600,
      top: 100,
      bottom: 600,
    } as DOMRect);
    const field = within(dialog).getAllByRole("textbox")[0];
    fireEvent.change(field, { target: { value: "unsaved" } });
    fireEvent.mouseDown(dialog, { clientX: 110, clientY: 110 });
    fireEvent.click(dialog, { clientX: 110, clientY: 110 });
    expect(screen.getByRole("dialog")).toBe(dialog);
    fireEvent.mouseDown(field, { clientX: 200, clientY: 200 });
    fireEvent.click(dialog, { clientX: 10, clientY: 10 });
    expect(screen.getByRole("dialog")).toBe(dialog);
    expect((field as HTMLInputElement).value).toBe("unsaved");
    expect(onSave).not.toHaveBeenCalled();
    backdrop(dialog);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

for (const name of [
  "Claude",
  "OpenAI-compatible",
  "OpenRouter",
  "Add API connection",
])
  it(`keeps ${name} modal and shows failure after a native close during save`, async () => {
    let reject!: (error: Error) => void;
    const onSave = setup(
      { ...config, engines: { ...config.engines, providers: [provider] } },
      vi.fn(
        () =>
          new Promise<void>((_, fail) => {
            reject = fail;
          }),
      ),
    );
    open(name);
    const dialog = screen.getByRole<HTMLDialogElement>("dialog");
    if (name === "Add API connection") {
      fireEvent.change(screen.getByLabelText("Name"), {
        target: { value: "New provider" },
      });
      fireEvent.change(screen.getByLabelText("API address"), {
        target: { value: "https://example.test/v1" },
      });
    }
    const field = within(dialog).getAllByRole<HTMLInputElement>("textbox")[0];
    const value =
      name === "Claude"
        ? "/edited/claude"
        : name === "OpenAI-compatible"
          ? "https://edited.test/v1"
          : "Edited connection";
    fireEvent.change(field, { target: { value } });
    save();
    // Model the browser's non-cancelable second Escape and native close.
    fireEvent(dialog, new Event("cancel", { cancelable: false }));
    dialog.open = false;
    fireEvent(dialog, new Event("close"));
    expect(screen.getByRole("dialog")).toBe(dialog);
    expect(dialog.open).toBe(true);
    expect(field.value).toBe(value);
    expect(
      within(dialog).getByRole<HTMLButtonElement>("button", { name: "Save" })
        .disabled,
    ).toBe(true);
    save();
    expect(onSave).toHaveBeenCalledOnce();
    reject(new Error("Save failed"));
    expect((await screen.findByRole("alert")).textContent).toBe("Save failed");
    expect(screen.getByRole("dialog")).toBe(dialog);
    expect(dialog.open).toBe(true);
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    open("OpenRouter");
    expect(screen.getByLabelText<HTMLInputElement>("Name").value).toBe(
      "OpenRouter",
    );
  });

it("limits login/program guidance to CLI dialogs and styles removal as quiet danger", () => {
  setup({ ...config, engines: { ...config.engines, providers: [provider] } });
  open("Claude");
  expect(
    screen.getByText(/Lists of models use the saved login and program/),
  ).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  open("OpenAI-compatible");
  expect(
    screen.queryByText(/Lists of models use the saved login and program/),
  ).toBeNull();
  expect(
    screen.getByText("Optional; a blank field uses what it shows."),
  ).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  open("OpenRouter");
  const remove = screen.getByRole("button", { name: "Remove connection" });
  expect(remove.classList.contains("btn-quiet")).toBe(true);
  expect(remove.classList.contains("btn-danger")).toBe(true);
});

it("ignores a queued cleanup close event after StrictMode reopens the dialog", async () => {
  vi.spyOn(HTMLDialogElement.prototype, "close").mockImplementation(function (
    this: HTMLDialogElement,
  ) {
    if (!this.open) return;
    this.open = false;
    queueMicrotask(() => this.dispatchEvent(new Event("close")));
  });
  render(
    <StrictMode>
      <EngineSettings config={config} onSave={vi.fn()} />
    </StrictMode>,
  );
  await act(async () => {
    open("Claude");
  });
  expect(
    screen.getByRole<HTMLDialogElement>("dialog", { name: "Edit Claude" }).open,
  ).toBe(true);
});

for (const failure of [false, true])
  it(`preserves page drafts when an engine save ${failure ? "fails" : "succeeds despite refresh failure"}`, async () => {
    let stored = {
      ...config,
      models: { suggestions: { engine: "", model: "old", effort: "" } },
    };
    const { calls } = recordFetch((path, options) => {
      if (path === "/api/config" && options?.method === "PUT") {
        if (failure) return reply({ error: "Invalid engine" }, 400);
        stored = JSON.parse(String(options.body));
        return reply({});
      }
      if (path === "/api/config") return reply(stored);
      return reply(
        path.includes("/api/models") ? { available: false, models: [] } : {},
      );
    });
    render(
      <Settings
        state={normalizeState({})}
        section="models"
        refresh={async () => {
          throw new Error("refresh failed");
        }}
      />,
    );
    const suggestion = await screen.findByLabelText<HTMLSelectElement>(
      /^Suggestions and loading lines/,
    );
    fireEvent.change(suggestion, { target: { value: "codex" } });
    open("Claude");
    fireEvent.change(screen.getByLabelText("Claude program"), {
      target: { value: "/new/claude" },
    });
    const dialogForm = screen.getByLabelText("Claude program").closest("form");
    expect(dialogForm).not.toBeNull();
    expect(
      screen.getByRole("form", { name: "Settings" }).contains(dialogForm),
    ).toBe(false);
    save();
    await waitFor(() =>
      expect(calls.filter((c) => c.options?.method === "PUT")).toHaveLength(1),
    );
    const body = JSON.parse(
      String(calls.find((c) => c.options?.method === "PUT")!.options!.body),
    );
    expect(body.models.suggestions.engine).toBe("");
    expect(body.engines.claude.bin).toBe("/new/claude");
    expect(
      screen.getByRole("region", { name: "Unsaved changes" }),
    ).toBeTruthy();
    expect(suggestion.value).toBe("codex");
    if (failure) {
      await screen.findByRole("alert");
      expect(screen.getByRole("dialog")).toBeTruthy();
      expect(stored.engines).toEqual(config.engines);
    } else {
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      expect(stored.engines!.claude).toEqual({ bin: "/new/claude" });
    }
  });
for (const failure of [false, true])
  it(`blocks all engine editors during a page save that ${failure ? "fails" : "succeeds"}`, async () => {
    let finish!: () => void;
    const { calls } = recordFetch(async (path, options) => {
      if (path === "/api/config" && options?.method === "PUT") {
        await new Promise<void>((resolve) => {
          finish = resolve;
        });
        return failure ? reply({ error: "Save failed" }, 400) : reply({});
      }
      if (path === "/api/config")
        return reply({
          ...config,
          engines: { ...config.engines, providers: [provider] },
        });
      return reply(
        path.includes("/api/models") ? { available: false, models: [] } : {},
      );
    });
    render(
      <Settings
        state={normalizeState({})}
        section="models"
        refresh={async () => {}}
      />,
    );
    fireEvent.change(
      await screen.findByLabelText(/^Suggestions and loading lines/),
      { target: { value: "codex" } },
    );
    fireEvent.click(
      within(screen.getByRole("region", { name: "Unsaved changes" })).getByRole(
        "button",
        { name: "Save" },
      ),
    );
    const buttons = [
      "Claude",
      "Codex",
      "Grok",
      "OpenAI-compatible",
      "OpenRouter",
      "Add API connection",
    ].map((name) =>
      screen.getByRole<HTMLButtonElement>("button", { name: new RegExp(name) }),
    );
    for (const button of buttons) {
      expect(button.disabled).toBe(true);
      fireEvent.click(button);
    }
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(calls.filter((c) => c.options?.method === "PUT")).toHaveLength(1);
    finish();
    await waitFor(() =>
      buttons.forEach((button) => expect(button.disabled).toBe(false)),
    );
    open("Claude");
    expect(screen.getByRole("dialog", { name: "Edit Claude" })).toBeTruthy();
  });
it("leaves Escape in an open dialog to the dialog while chat is open", () => {
  vi.stubGlobal(
    "matchMedia",
    vi.fn(() => ({
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })),
  );
  function Pane() {
    const chat = useChatPane();
    return (
      <>
        <button onClick={chat.toggle}>Toggle</button>
        <span>{chat.drawerOpen ? "Drawer open" : "Drawer closed"}</span>
        <aside ref={chat.conversation}>
          <button>Chat</button>
        </aside>
        <dialog open>
          <button>Dialog control</button>
        </dialog>
      </>
    );
  }
  render(<Pane />);
  fireEvent.click(screen.getByText("Toggle"));
  const event = new KeyboardEvent("keydown", {
    key: "Escape",
    bubbles: true,
    cancelable: true,
  });
  screen.getByText("Dialog control").dispatchEvent(event);
  expect(event.defaultPrevented).toBe(false);
  expect(screen.getByText("Drawer open")).toBeTruthy();
  fireEvent.keyDown(screen.getByText("Chat"), { key: "Escape" });
  expect(screen.getByText("Drawer closed")).toBeTruthy();
});

it.each([true, false])(
  "shows Codex browser bridge status (%s)",
  async (usable) => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(async () => ({
        ok: true,
        json: async () => ({
          usable,
          reason: "No bridge is declared.",
          home: "/owner/.codex",
        }),
      })),
    );
    setup();
    open("Codex");
    expect(
      await screen.findByText(
        usable
          ? /Browser bridge: usable/
          : /Browser bridge: not usable.*No bridge is declared/,
      ),
    ).toBeTruthy();
    expect(screen.getByText("/owner/.codex")).toBeTruthy();
  },
);

it("checks the saved bridge folder again when Codex settings reopen", async () => {
  let home = "/owner/.codex";
  vi.mocked(fetch).mockImplementation(
    async () =>
      ({
        ok: true,
        json: async () => ({ usable: true, reason: "", home }),
      }) as Response,
  );
  const onSave = setup(
    config,
    vi.fn().mockImplementation(async () => {
      home = "/other/.codex";
    }),
  );
  open("Codex");
  await screen.findByText("/owner/.codex");
  expect(screen.getByText(/reopen Codex settings to check/)).toBeTruthy();
  fireEvent.change(screen.getByLabelText(/Browser bridge folder/), {
    target: { value: "/other/.codex" },
  });
  save();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(onSave).toHaveBeenCalledWith({
    ...config.engines,
    codex: {
      home: "/test/login",
      usage_floor: { "5h_percent": 20 },
      browser_bridge_home: "/other/.codex",
    },
  });
  open("Codex");
  await screen.findByText("/other/.codex");
});

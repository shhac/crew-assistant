// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import {
  ChatPanel,
  commandIn,
  SUGGESTION_DELAY,
  wakeHappenings,
  wakeSummary,
} from "./ChatPanel";
import { ConversationMarkdown } from "./ConversationMarkdown";
import {
  normalizeState,
  type ChatSession,
  type ChatTurn,
  type State,
} from "./api";
import { fullDateLabel } from "./ui";
beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
const initial = () =>
  normalizeState({ assistant: { name: "Iris", personality: "" } });
function panel(
  state = initial(),
  refresh = vi.fn(async () => {}),
  view = "Overview",
) {
  return (
    <ChatPanel
      state={state}
      refresh={refresh}
      expanded={false}
      onExpand={() => {}}
      onClose={() => {}}
      view={view}
    />
  );
}
function typeAndSend(text: string) {
  const input = screen.getByRole("textbox");
  fireEvent.change(input, { target: { value: text } });
  fireEvent.keyDown(input, { key: "Enter", code: "Enter" });
  return input as HTMLTextAreaElement;
}
const result = (body: unknown) => ({
  ok: true,
  status: 200,
  json: async () => body,
});
const tick = async (ms = 3000) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};
type Reply = { ok: boolean; status: number; json: () => Promise<unknown> };
function backend(
  seed: ChatTurn[] = [],
  suggest: (after: string) => Reply | Promise<Reply> = (after) =>
    result({ after, suggestion: "" }),
) {
  const turns = [...seed];
  const fetch = vi.fn(async (path: string, options?: RequestInit) => {
    if (path === "/api/chat/turns")
      return result({ turns: turns.map((t) => ({ ...t })) });
    if (path === "/api/chat/suggestion")
      return suggest(JSON.parse(options!.body as string).after);
    if (options?.method === "POST") {
      const body = JSON.parse(options.body as string);
      let turn = turns.find((t) => t.id === body.id);
      if (!turn) {
        turn = {
          ...body,
          status: turns.some((t) => t.status === "running")
            ? "queued"
            : "running",
          created_at: new Date().toISOString(),
          user_message_id: `user-${body.id}`,
          events: [],
        };
        turns.push(turn!);
      }
      return result({ ...turn });
    }
    if (options?.method === "DELETE") {
      const turn = turns.find((t) => path.endsWith(t.id))!;
      turn.status = "cancelled";
      return result({ ...turn });
    }
    throw new Error("Unexpected request");
  });
  vi.stubGlobal("fetch", fetch);
  return {
    turns,
    fetch,
    posts: () =>
      fetch.mock.calls.filter(
        ([p, o]) => o?.method === "POST" && p === "/api/chat/messages",
      ),
    suggestions: () =>
      fetch.mock.calls.filter(([p]) => p === "/api/chat/suggestion"),
  };
}
const savedTurn = (overrides: Partial<ChatTurn> = {}): ChatTurn => ({
  id: "turn-1",
  message: "Prepare the project",
  status: "running",
  created_at: "2026-09-16T12:00:00Z",
  user_message_id: "user-1",
  revision: 0,
  events: [],
  ...overrides,
});
describe("conversation", () => {
  it("shows messages immediately, queues while a reply runs, and deduplicates using exact server IDs", async () => {
    const server = backend();
    const state = initial();
    const view = render(panel(state));
    const input = typeAndSend("Please coordinate this");
    expect(
      within(screen.getByRole("log")).getAllByText("Please coordinate this"),
    ).toHaveLength(1);
    expect(input.value).toBe("");
    await tick(0);
    expect(screen.getByText("Iris is working")).toBeTruthy();
    typeAndSend("Another thought");
    await tick(0);
    expect(server.posts()).toHaveLength(2);
    // A queued message now waits in the queue, where its position is its
    // status and it can be changed before it runs.
    const queue = screen.getByRole("region", { name: "Queued messages" });
    expect(within(queue).getByText("Another thought")).toBeTruthy();
    expect(within(queue).getByText("Up next (1)")).toBeTruthy();
    state.messages = server.turns.map((t) => ({
      id: t.user_message_id!,
      role: "user",
      content: t.message,
      created_at: t.created_at,
    }));
    view.rerender(panel(state));
    expect(
      within(screen.getByRole("log")).getAllByText("Please coordinate this"),
    ).toHaveLength(1);
    expect(
      within(screen.getByRole("log")).getAllByText("Another thought"),
    ).toHaveLength(1);
    expect(input.value).toBe("");
  });
  it("shows a wake-up as the daemon's, not the owner's", () => {
    const state = initial();
    state.messages = [
      {
        id: "w1",
        role: "user",
        origin: "wake",
        content: "[Wake-up from the daemon] wake-1 fired",
        created_at: "2026-09-24T09:18:23Z",
      },
      {
        id: "u1",
        role: "user",
        content: "Thanks",
        created_at: "2026-09-24T09:19:00Z",
      },
    ];
    render(panel(state));
    const log = screen.getByRole("log");
    expect(within(log).getByText("Wake-up")).toBeTruthy();
    expect(within(log).getAllByText("You")).toHaveLength(1);
    const wake = log.querySelector("div.wake")!;
    expect(wake.querySelector(".wake-head .wake-line")?.textContent).toBe(
      "Checked in",
    );
    // The note written for the model is never shown to the owner.
    expect(log.textContent).not.toContain("wake-1 fired");
    expect(log.querySelector("details")).toBeNull();
    expect(log.querySelectorAll("article.message")).toHaveLength(1);
  });
  it("puts the assistant's avatar in the header and its bylines only", () => {
    const state = initial();
    state.assistant.avatar_svg = '<svg xmlns="http://www.w3.org/2000/svg"/>';
    state.messages = [
      {
        id: "w1",
        role: "user",
        origin: "wake",
        content: "[Wake-up from the daemon] wake-1 fired",
      },
      { id: "u1", role: "user", content: "Hello" },
      { id: "a1", role: "assistant", content: "Hi" },
    ];
    render(panel(state));
    const url = `data:image/svg+xml,${encodeURIComponent(state.assistant.avatar_svg)}`;
    const head = screen.getByRole("heading", { name: "Iris" }).parentElement!;
    expect(head.querySelector("img")?.getAttribute("src")).toBe(url);
    expect(head.querySelector("img")?.getAttribute("width")).toBe("20");
    const log = screen.getByRole("log");
    const [yours, theirs] = log.querySelectorAll("article.message");
    expect(yours.querySelector("img")).toBeNull();
    expect(theirs.querySelector(".message-by img")?.getAttribute("src")).toBe(
      url,
    );
    expect(log.querySelector("div.wake img")).toBeNull();
  });
  it("does not submit Shift+Enter or an IME composition", async () => {
    const server = backend();
    render(panel());
    const input = screen.getByRole("textbox");
    fireEvent.change(input, { target: { value: "A draft" } });
    fireEvent.keyDown(input, { key: "Enter", shiftKey: true });
    fireEvent.keyDown(input, { key: "Enter", isComposing: true });
    fireEvent.keyDown(input, { key: "Enter", keyCode: 229 });
    expect(server.posts()).toHaveLength(0);
    fireEvent.keyDown(input, { key: "Enter" });
    await tick(0);
    expect(server.posts()).toHaveLength(1);
  });
  it("never automatically replays ambiguous delivery and retries explicitly with the same id", async () => {
    const server = backend();
    const original = server.fetch.getMockImplementation()!;
    let reject = true;
    server.fetch.mockImplementation(async (path, options) => {
      if (options?.method === "POST" && reject) {
        reject = false;
        throw new Error("Connection lost");
      }
      return original(path, options);
    });
    render(panel());
    const input = typeAndSend("My original request");
    fireEvent.change(input, { target: { value: "A second thought" } });
    await tick(0);
    expect(screen.getByText("Not confirmed yet")).toBeTruthy();
    expect(
      screen.getByText("Retrying is safe: it can't start a second reply."),
    ).toBeTruthy();
    expect(input.value).toBe("A second thought");
    await tick(6000);
    expect(server.posts()).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await tick(0);
    expect(server.posts()).toHaveLength(2);
    const bodies = server
      .posts()
      .map(([, options]) => JSON.parse(options!.body as string));
    expect(bodies[0]).toEqual(bodies[1]);
    expect(input.value).toBe("A second thought");
  });
  it("loads durable queued turns and cancels only the selected pending message", async () => {
    const server = backend([
      savedTurn(),
      savedTurn({
        id: "turn-2",
        user_message_id: "user-2",
        message: "Then review it",
        status: "queued",
      }),
    ]);
    render(panel());
    await tick(0);
    expect(screen.getByText("Then review it")).toBeTruthy();
    fireEvent.click(
      screen.getByRole("button", {
        name: "Remove queued message: Then review it",
      }),
    );
    await tick(0);
    expect(screen.getByText("Cancelled")).toBeTruthy();
    expect(server.turns[0].status).toBe("running");
    expect(
      server.fetch.mock.calls.filter(([, o]) => o?.method === "DELETE"),
    ).toHaveLength(1);
  });
  it("polls real tool activity beside its turn and updates status through completion", async () => {
    const server = backend([
      savedTurn({
        loading_phrase: "Gathering the threads…",
        events: [
          {
            id: "event-1",
            tool: "create_project",
            label: "Creating project",
            status: "running",
            started_at: "2026-09-16T12:00:01Z",
          },
        ],
      }),
    ]);
    const refresh = vi.fn(async () => {});
    render(panel(initial(), refresh));
    await tick(0);
    const activity = screen.getByRole("group", {
      name: "What the assistant did",
    });
    expect(within(activity).getByText("Creating project…")).toBeTruthy();
    expect(within(activity).getByText("Creating project")).toBeTruthy();
    expect(within(activity).getByText("· Working")).toBeTruthy();
    expect(screen.getByText("Gathering the threads…")).toBeTruthy();
    server.turns[0] = {
      ...server.turns[0],
      status: "completed",
      assistant_message_id: "answer-1",
      events: [{ ...server.turns[0].events[0], status: "completed" }],
    };
    await tick();
    // Once replied, the steps fold into a summary of the work, one click away.
    expect(within(activity).getByText("Worked · 1 step")).toBeTruthy();
    expect(within(activity).queryByText("· Working")).toBeNull();
    expect(screen.queryByText("Gathering the threads…")).toBeNull();
    expect(refresh.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(server.posts()).toHaveLength(0);
  });
  it("shows interrupted and failed tool history without retrying work or matching identical text", async () => {
    const server = backend([
      savedTurn({
        status: "interrupted",
        error: "Daemon restarted",
        events: [
          {
            id: "event-1",
            tool: "create_project",
            label: "Creating project",
            status: "failed",
            started_at: "2026-09-16T12:00:01Z",
          },
        ],
      }),
    ]);
    const state = initial();
    state.messages = [
      {
        id: "old-unrelated-message",
        role: "user",
        content: "Prepare the project",
      },
    ];
    render(panel(state));
    await tick(0);
    expect(
      within(screen.getByRole("log")).getAllByText("Prepare the project"),
    ).toHaveLength(2);
    expect(
      screen.getByText("Stopped before finishing. Not retried."),
    ).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toBe("Daemon restarted");
    // The failed step is shown without opening the summary.
    const activity = screen.getByRole("group", {
      name: "What the assistant did",
    });
    const problems = activity.querySelector<HTMLElement>("ul.tools-problems")!;
    expect(within(problems).getByText("· Failed")).toBeTruthy();
    expect(within(activity).queryByText(/create_project/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
    await tick();
    expect(server.posts()).toHaveLength(0);
  });
  it("restores a definitively rejected message without replaying it", async () => {
    const server = backend();
    const original = server.fetch.getMockImplementation()!;
    server.fetch.mockImplementation(async (path, options) =>
      options?.method === "POST"
        ? {
            ok: false,
            status: 429,
            json: async () => ({ error: "The message queue is full." }),
          }
        : original(path, options),
    );
    render(panel());
    const input = typeAndSend("One more thing");
    fireEvent.change(input, { target: { value: "A next draft" } });
    await tick(0);
    expect(screen.getByText("Not sent")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    expect(input.value).toBe("A next draft\n\nOne more thing");
    expect(document.activeElement).toBe(input);
    expect(server.posts()).toHaveLength(1);
  });
  it("discards a rejected message without replaying it or touching the draft", async () => {
    const server = backend();
    const original = server.fetch.getMockImplementation()!;
    server.fetch.mockImplementation(async (path, options) =>
      options?.method === "POST"
        ? {
            ok: false,
            status: 429,
            json: async () => ({ error: "The message queue is full." }),
          }
        : original(path, options),
    );
    render(panel());
    const input = typeAndSend("Never mind this");
    fireEvent.change(input, { target: { value: "Something else" } });
    await tick(0);
    fireEvent.click(screen.getByRole("button", { name: "Discard" }));
    expect(screen.queryByText("Never mind this")).toBeNull();
    expect(screen.queryByText("Not sent")).toBeNull();
    expect(input.value).toBe("Something else");
    await tick();
    expect(server.posts()).toHaveLength(1);
  });
  it("does not enqueue a draft twice when Enter repeats before React renders", async () => {
    const server = backend();
    render(panel());
    const input = screen.getByRole("textbox");
    fireEvent.change(input, { target: { value: "One request" } });
    act(() => {
      fireEvent.keyDown(input, { key: "Enter" });
      fireEvent.keyDown(input, { key: "Enter" });
    });
    await tick(0);
    expect(server.posts()).toHaveLength(1);
  });
  it("does not let an old poll undo a confirmed cancellation", async () => {
    const turn = savedTurn({ status: "queued" });
    const server = backend([turn]);
    render(panel());
    await tick(0);
    const stale = { ...turn };
    const original = server.fetch.getMockImplementation()!;
    let release!: (value: ReturnType<typeof result>) => void;
    server.fetch.mockImplementation((path, options) =>
      path === "/api/chat/turns"
        ? new Promise((resolve) => {
            release = resolve;
          })
        : original(path, options),
    );
    await tick();
    fireEvent.click(
      screen.getByRole("button", {
        name: "Remove queued message: Prepare the project",
      }),
    );
    await tick(0);
    expect(screen.getByText("Cancelled")).toBeTruthy();
    await act(async () => {
      release(result({ turns: [stale] }));
    });
    expect(screen.getByText("Cancelled")).toBeTruthy();
    expect(
      screen.queryByRole("region", { name: "Queued messages" }),
    ).toBeNull();
  });
  it("keeps uncertain original delivery uncertain when its explicit retry is rejected", async () => {
    const server = backend();
    const original = server.fetch.getMockImplementation()!;
    let attempt = 0;
    server.fetch.mockImplementation(async (path, options) => {
      if (options?.method !== "POST") return original(path, options);
      if (++attempt === 1) throw new Error("Connection lost");
      return {
        ok: false,
        status: 401,
        json: async () => ({ error: "Session expired." }),
      };
    });
    render(panel());
    typeAndSend("Start the work");
    await tick(0);
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await tick(0);
    expect(screen.getByText("Not confirmed yet")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
    expect(server.posts()).toHaveLength(2);
  });
  it("waits only for the first acknowledgement before delivering the next optimistic message", async () => {
    const server = backend();
    const original = server.fetch.getMockImplementation()!;
    let release!: () => void;
    let first = true;
    server.fetch.mockImplementation(async (path, options) => {
      if (options?.method === "POST" && first) {
        first = false;
        await new Promise<void>((resolve) => {
          release = resolve;
        });
      }
      return original(path, options);
    });
    render(panel());
    typeAndSend("First request");
    typeAndSend("Second request");
    expect(screen.getByText("First request")).toBeTruthy();
    expect(screen.getByText("Second request")).toBeTruthy();
    expect(screen.getByText("Waiting to send")).toBeTruthy();
    expect(server.posts()).toHaveLength(1);
    await act(async () => {
      release();
    });
    expect(server.posts()).toHaveLength(2);
    expect(server.turns.map((t) => t.message)).toEqual([
      "First request",
      "Second request",
    ]);
    expect(server.turns[0].status).toBe("running");
    expect(server.turns[1].status).toBe("queued");
  });
  it("cancels a message still waiting to send, without it ever being sent", async () => {
    const server = backend();
    const original = server.fetch.getMockImplementation()!;
    let release!: () => void;
    let first = true;
    server.fetch.mockImplementation(async (path, options) => {
      if (options?.method === "POST" && first) {
        first = false;
        await new Promise<void>((resolve) => {
          release = resolve;
        });
      }
      return original(path, options);
    });
    render(panel());
    typeAndSend("First request");
    typeAndSend("Second request");
    expect(screen.getByText("Sending…")).toBeTruthy();
    fireEvent.click(
      screen.getByRole("button", { name: "Cancel message: Second request" }),
    );
    expect(screen.queryByText("Second request")).toBeNull();
    await act(async () => {
      release();
    });
    await tick();
    expect(server.posts()).toHaveLength(1);
    expect(server.turns.map((t) => t.message)).toEqual(["First request"]);
    expect(
      server.fetch.mock.calls.some(([, o]) => o?.method === "DELETE"),
    ).toBe(false);
  });
  it("holds later submissions until an uncertain delivery is resolved by its same-id retry", async () => {
    const server = backend();
    const original = server.fetch.getMockImplementation()!;
    let reject = true;
    server.fetch.mockImplementation(async (path, options) => {
      if (options?.method === "POST" && reject) {
        reject = false;
        throw new Error("Connection lost");
      }
      return original(path, options);
    });
    render(panel());
    typeAndSend("First request");
    typeAndSend("Second request");
    await tick(0);
    expect(server.posts()).toHaveLength(1);
    expect(screen.getByText("Waiting to send")).toBeTruthy();
    await tick();
    expect(server.posts()).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await tick(0);
    expect(server.posts()).toHaveLength(3);
    expect(server.turns.map((t) => t.message)).toEqual([
      "First request",
      "Second request",
    ]);
    expect(JSON.parse(server.posts()[0][1]!.body as string).id).toBe(
      JSON.parse(server.posts()[1][1]!.body as string).id,
    );
  });
  it("shows interrupted tool outcomes as unconfirmed, rather than a known failure", async () => {
    backend([
      savedTurn({
        status: "interrupted",
        events: [
          {
            id: "tool-1",
            tool: "create_project",
            label: "Creating project",
            status: "interrupted",
            started_at: "2026-09-16T12:00:01Z",
          },
        ],
      }),
    ]);
    render(panel());
    await tick(0);
    expect(
      screen.getAllByText("· Stopped; outcome not confirmed").length,
    ).toBeGreaterThan(0);
    expect(screen.queryByText("· Failed")).toBeNull();
    expect(
      document.querySelector(".tools-summary summary")?.textContent,
    ).not.toContain("Creating project");
  });
  it("times out only the enqueue acknowledgement and keeps delivery uncertain", async () => {
    const server = backend();
    const original = server.fetch.getMockImplementation()!;
    server.fetch.mockImplementation((path, options) =>
      options?.method === "POST"
        ? new Promise((_, reject) => {
            options.signal?.addEventListener("abort", () =>
              reject(new DOMException("Aborted", "AbortError")),
            );
          })
        : original(path, options),
    );
    render(panel());
    typeAndSend("A stalled send");
    typeAndSend("Keep this next");
    await tick(15_000);
    expect(screen.getByText("Not confirmed yet")).toBeTruthy();
    expect(screen.getByText("Checking whether it arrived.")).toBeTruthy();
    expect(
      screen.getByText(
        "The next messages wait until this one is confirmed. Keep this page open.",
      ),
    ).toBeTruthy();
    expect(screen.getByText("Waiting to send")).toBeTruthy();
    expect(server.posts()).toHaveLength(1);
    expect(
      server.fetch.mock.calls.some(
        ([, options]) => options?.method === "DELETE",
      ),
    ).toBe(false);
  });
  it("renders formatting and project names while suppressing unsafe HTML, URLs and image requests", () => {
    const open = vi.fn();
    const state: State = initial();
    state.projects = [
      { id: "proj-123", title: "Garden planner" } as State["projects"][number],
    ];
    const view = render(
      <ConversationMarkdown
        projects={state.projects}
        onProjectOpen={open}
        content={
          '**Ready** with `notes`\n\n- A task\n\n[proj-123](#/projects/proj-123)\n\n[Unsafe](javascript:alert%281%29)\n\n[Docs](https://example.org)\n\n<img src="x" onerror="alert(1)"><script>alert(1)</script>\n\n![tracking](https://example.org/pixel.png)'
        }
      />,
    );
    expect(view.container.querySelector("strong")?.textContent).toBe("Ready");
    expect(view.container.querySelector("code")?.textContent).toBe("notes");
    expect(view.container.querySelector("li")?.textContent).toBe("A task");
    expect(view.container.querySelectorAll("script,img")).toHaveLength(0);
    expect(screen.getByText("Unsafe").getAttribute("href")).toBe("");
    expect(screen.getByRole("link", { name: "Docs" }).getAttribute("rel")).toBe(
      "noopener noreferrer",
    );
    fireEvent.click(screen.getByRole("link", { name: "Garden planner" }));
    expect(open).toHaveBeenCalledWith("proj-123");
  });
});
describe("composer attachments", () => {
  const textFile = (content: string, name: string, type = "text/plain") =>
    new File([content], name, { type });
  function drop(files: File[]) {
    const form = screen.getByRole("textbox").closest("form")!;
    fireEvent.dragOver(form, { dataTransfer: { files, types: ["Files"] } });
    return fireEvent.drop(form, {
      dataTransfer: { files, types: ["Files"] },
    });
  }
  function paste(files: File[], text = "") {
    return fireEvent.paste(screen.getByRole("textbox"), {
      clipboardData: {
        files,
        types: files.length ? ["Files"] : ["text/plain"],
        getData: (type: string) => (type === "text/plain" ? text : ""),
      },
    });
  }
  const attachments = () => screen.queryByRole("list", { name: "Attachments" });
  const sentMessages = (server: ReturnType<typeof backend>) =>
    server
      .posts()
      .map(([, options]) => JSON.parse(options!.body as string).message);
  it("attaches dropped text files, refuses others with a reason, and sends what remains", async () => {
    const server = backend();
    render(panel());
    expect(
      drop([
        textFile("# Plan\n", "plan.md", ""),
        textFile("a,b\n", "data.csv", "text/csv"),
        new File([new Uint8Array([137, 80, 78, 71])], "shot.png", {
          type: "image/png",
        }),
      ]),
    ).toBe(false);
    await tick(0);
    const list = attachments()!;
    expect(within(list).getByText("plan.md")).toBeTruthy();
    expect(within(list).getByText("data.csv")).toBeTruthy();
    expect(within(list).queryByText("shot.png")).toBeNull();
    expect(screen.getByRole("alert").textContent).toContain(
      "shot.png can't be attached: only text files can be sent (image/png is not supported yet).",
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Remove attachment data.csv" }),
    );
    expect(within(list).queryByText("data.csv")).toBeNull();
    typeAndSend("Use this plan");
    await tick(0);
    expect(sentMessages(server)).toEqual([
      "Use this plan\n\nAttached file: plan.md\n```\n# Plan\n```",
    ]);
    expect(attachments()).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });
  it("attaches pasted files, sends them without text, and leaves ordinary text paste alone", async () => {
    const server = backend();
    render(panel());
    expect(paste([], "Just words")).toBe(true);
    await tick(0);
    expect(attachments()).toBeNull();
    expect(paste([textFile("Remember the milk", "note.txt")])).toBe(false);
    await tick(0);
    expect(within(attachments()!).getByText("note.txt")).toBeTruthy();
    const send = screen.getByRole("button", { name: "Send" });
    expect((send as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(send);
    await tick(0);
    expect(sentMessages(server)).toEqual([
      "Attached file: note.txt\n```\nRemember the milk\n```",
    ]);
  });
  it("pastes text as usual and still handles files that come with it", async () => {
    const server = backend();
    render(panel());
    // A copied file carries its name as text: the name pastes, the file attaches.
    expect(paste([textFile("note", "copied.txt")], "copied.txt")).toBe(true);
    await tick(0);
    expect(within(attachments()!).getByText("copied.txt")).toBeTruthy();
    // A file that can't be attached is reported, not silently dropped.
    const image = new File([new Uint8Array([137, 80])], "render.png", {
      type: "image/png",
    });
    expect(paste([image], "Quarterly figures")).toBe(true);
    await tick(0);
    expect(screen.getByRole("alert").textContent).toContain(
      "render.png can't be attached: only text files can be sent",
    );
    expect(within(attachments()!).getAllByRole("listitem")).toHaveLength(1);
    typeAndSend("See attached");
    await tick(0);
    expect(sentMessages(server)).toEqual([
      "See attached\n\nAttached file: copied.txt\n```\nnote\n```",
    ]);
  });
  it("reports size limits and unreadable files, and keeps attachments when a message is too large", async () => {
    const server = backend();
    render(panel());
    drop([
      textFile("x".repeat(24_001), "huge.txt"),
      new File([new Uint8Array([0xff, 0xfe, 0x00])], "broken.txt", {
        type: "text/plain",
      }),
    ]);
    await tick(0);
    const errors = screen.getByRole("alert").textContent;
    expect(errors).toContain(
      "huge.txt can't be attached: it is 24,001 bytes, and a message can carry at most 24,000 bytes.",
    );
    expect(errors).toContain(
      "broken.txt can't be attached: it is not readable UTF-8 text.",
    );
    expect(attachments()).toBeNull();
    drop([
      textFile("a".repeat(13_000), "one.txt"),
      textFile("b".repeat(13_000), "two.txt"),
    ]);
    await tick(0);
    expect(screen.queryByRole("alert")).toBeNull();
    typeAndSend("Both please");
    await tick(0);
    expect(server.posts()).toHaveLength(0);
    expect(screen.getByRole("alert").textContent).toContain(
      "the limit is 24,000 bytes. Remove an attachment or shorten the message.",
    );
    expect(within(attachments()!).getAllByRole("listitem")).toHaveLength(2);
    expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe(
      "Both please",
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Remove attachment two.txt" }),
    );
    typeAndSend("Both please");
    await tick(0);
    expect(server.posts()).toHaveLength(1);
  });
  it("keeps a refusal when an earlier, slower read finishes after it", async () => {
    backend();
    render(panel());
    const slow = textFile("Slow notes", "slow.txt");
    let finish!: () => void;
    const contents = await slow.arrayBuffer();
    Object.defineProperty(slow, "arrayBuffer", {
      value: () =>
        new Promise<ArrayBuffer>((resolve) => {
          finish = () => resolve(contents);
        }),
    });
    drop([slow]);
    await tick(0);
    expect(screen.getByText("Reading files…")).toBeTruthy();
    drop([
      new File([new Uint8Array([137])], "chart.png", { type: "image/png" }),
    ]);
    await tick(0);
    expect(screen.getByRole("alert").textContent).toContain(
      "chart.png can't be attached",
    );
    await act(async () => finish());
    await tick(0);
    expect(within(attachments()!).getByText("slow.txt")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain(
      "chart.png can't be attached",
    );
    // A new batch, once nothing is being read, starts with a clean slate.
    drop([textFile("fresh", "fresh.txt")]);
    await tick(0);
    expect(screen.queryByRole("alert")).toBeNull();
  });
  it("restores a refused message's attachments to the composer", async () => {
    const server = backend();
    const original = server.fetch.getMockImplementation()!;
    server.fetch.mockImplementation(async (path, options) =>
      options?.method === "POST"
        ? {
            ok: false,
            status: 429,
            json: async () => ({ error: "The message queue is full." }),
          }
        : original(path, options),
    );
    render(panel());
    drop([textFile("draft", "draft.md", "text/markdown")]);
    await tick(0);
    const input = typeAndSend("Review this");
    await tick(0);
    expect(screen.getByText("Not sent")).toBeTruthy();
    expect(attachments()).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    expect(input.value).toBe("Review this");
    expect(within(attachments()!).getByText("draft.md")).toBeTruthy();
  });
});
describe("next-message suggestions", () => {
  const defaultPlaceholder = "Message Iris";
  // A conversation that has settled on the assistant's reply `reply-1`.
  function settled(extra: State["messages"] = []) {
    const state = initial();
    state.messages = [
      {
        id: "user-1",
        role: "user",
        content: "Plan the garden",
        created_at: "2026-09-16T12:00:00Z",
      },
      {
        id: "reply-1",
        role: "assistant",
        content: "Here is a planting plan.",
        created_at: "2026-09-16T12:00:05Z",
      },
      ...extra,
    ];
    return state;
  }
  const offering = (text: string) => (after: string) =>
    result({ after, suggestion: text });
  function deferred() {
    let resolve!: (reply: Reply) => void;
    const promise = new Promise<Reply>((r) => (resolve = r));
    return { promise, resolve };
  }
  const input = () => screen.getByRole("textbox") as HTMLTextAreaElement;

  it("waits for the conversation to settle, then shows an uncommitted suggestion that Tab accepts without sending", async () => {
    const server = backend([], offering("What should I plant first?"));
    render(panel(settled()));
    await tick(SUGGESTION_DELAY - 100);
    expect(server.suggestions()).toHaveLength(0);
    await tick(100);
    expect(server.suggestions()).toHaveLength(1);
    expect(JSON.parse(server.suggestions()[0][1]!.body as string)).toEqual({
      after: "reply-1",
    });
    expect(input().placeholder).toBe("What should I plant first?");
    expect(input().value).toBe("");
    expect(document.querySelector(".composer-hint")?.textContent).toBe(
      "Tab takes the suggestion",
    );
    // Enter on an empty draft never sends the suggestion.
    fireEvent.keyDown(input(), { key: "Enter" });
    await tick(0);
    expect(server.posts()).toHaveLength(0);
    // Tab accepts it into an editable draft, still unsent.
    expect(fireEvent.keyDown(input(), { key: "Tab" })).toBe(false);
    expect(input().value).toBe("What should I plant first?");
    expect(input().placeholder).toBe(defaultPlaceholder);
    await tick(0);
    expect(server.posts()).toHaveLength(0);
    fireEvent.change(input(), {
      target: { value: "What should I plant first? Tomatoes?" },
    });
    fireEvent.keyDown(input(), { key: "Enter" });
    await tick(0);
    expect(JSON.parse(server.posts()[0][1]!.body as string).message).toBe(
      "What should I plant first? Tomatoes?",
    );
  });

  it("dismisses the suggestion when the owner types and does not bring it back", async () => {
    const server = backend([], offering("What should I plant first?"));
    render(panel(settled()));
    await tick(SUGGESTION_DELAY);
    expect(input().placeholder).toBe("What should I plant first?");
    fireEvent.change(input(), { target: { value: "M" } });
    expect(input().value).toBe("M");
    expect(input().placeholder).toBe(defaultPlaceholder);
    fireEvent.change(input(), { target: { value: "" } });
    await tick(SUGGESTION_DELAY * 3);
    expect(input().placeholder).toBe(defaultPlaceholder);
    expect(server.suggestions()).toHaveLength(1);
  });

  it("leaves Tab to normal focus navigation when there is no suggestion", async () => {
    backend();
    render(panel(settled()));
    await tick(SUGGESTION_DELAY);
    expect(input().placeholder).toBe(defaultPlaceholder);
    expect(fireEvent.keyDown(input(), { key: "Tab" })).toBe(true);
    expect(input().value).toBe("");
    // With a draft, Tab is never taken either.
    fireEvent.change(input(), { target: { value: "My own words" } });
    expect(fireEvent.keyDown(input(), { key: "Tab" })).toBe(true);
    expect(input().value).toBe("My own words");
  });

  it("never asks while a reply is pending or being written, then asks about the new reply", async () => {
    const running = savedTurn({ id: "turn-2", user_message_id: "user-2" });
    const server = backend([running], offering("Thanks, what next?"));
    const pending = settled([
      {
        id: "user-2",
        role: "user",
        content: "Prepare the project",
        created_at: "2026-09-16T12:01:00Z",
      },
    ]);
    const view = render(panel(pending));
    await tick(SUGGESTION_DELAY * 5);
    expect(server.suggestions()).toHaveLength(0);
    // The reply is in the thread but its turn has not yet been seen to finish.
    const answered = settled([
      ...pending.messages.slice(2),
      {
        id: "reply-2",
        role: "assistant",
        content: "Prepared.",
        created_at: "2026-09-16T12:01:30Z",
      },
    ]);
    view.rerender(panel(answered));
    await tick(SUGGESTION_DELAY);
    expect(server.suggestions()).toHaveLength(0);
    server.turns[0].status = "completed";
    server.turns[0].assistant_message_id = "reply-2";
    await tick(3000);
    await tick(SUGGESTION_DELAY);
    expect(server.suggestions()).toHaveLength(1);
    expect(JSON.parse(server.suggestions()[0][1]!.body as string).after).toBe(
      "reply-2",
    );
    expect(input().placeholder).toBe("Thanks, what next?");
    // Sending a message unsettles the conversation and discards it.
    fireEvent.keyDown(input(), { key: "Tab" });
    fireEvent.keyDown(input(), { key: "Enter" });
    expect(input().value).toBe("");
    expect(input().placeholder).toBe(defaultPlaceholder);
  });

  it("never overwrites a draft typed while the suggestion was being written", async () => {
    const reply = deferred();
    backend([], () => reply.promise);
    render(panel(settled()));
    await tick(SUGGESTION_DELAY);
    fireEvent.change(input(), { target: { value: "My own idea" } });
    reply.resolve(result({ after: "reply-1", suggestion: "Something else" }));
    await tick(0);
    expect(input().value).toBe("My own idea");
    fireEvent.change(input(), { target: { value: "" } });
    await tick(0);
    expect(input().placeholder).toBe(defaultPlaceholder);
  });

  it("drops a suggestion that arrives after the conversation changed", async () => {
    const reply = deferred();
    const server = backend([], () => reply.promise);
    const view = render(panel(settled()));
    await tick(SUGGESTION_DELAY);
    expect(server.suggestions()).toHaveLength(1);
    // A newer reply lands (for example from Slack) before the answer returns.
    view.rerender(
      panel(
        settled([
          {
            id: "reply-2",
            role: "assistant",
            content: "One more thing.",
            created_at: "2026-09-16T12:02:00Z",
          },
        ]),
      ),
    );
    reply.resolve(result({ after: "reply-1", suggestion: "Stale idea" }));
    await tick(0);
    expect(input().placeholder).toBe(defaultPlaceholder);
  });

  it("discards a visible suggestion on navigation without asking again", async () => {
    const server = backend([], offering("What should I plant first?"));
    const refresh = vi.fn(async () => {});
    const view = render(panel(settled(), refresh, "Overview"));
    await tick(SUGGESTION_DELAY);
    expect(input().placeholder).toBe("What should I plant first?");
    view.rerender(panel(settled(), refresh, "Projects"));
    expect(input().placeholder).toBe(defaultPlaceholder);
    expect(fireEvent.keyDown(input(), { key: "Tab" })).toBe(true);
    await tick(SUGGESTION_DELAY * 3);
    expect(server.suggestions()).toHaveLength(1);
    expect(input().placeholder).toBe(defaultPlaceholder);
  });

  it("keeps the composer usable when generation fails", async () => {
    const server = backend([], () => ({
      ok: false,
      status: 502,
      json: async () => ({ error: "No suggestion this time." }),
    }));
    render(panel(settled()));
    await tick(SUGGESTION_DELAY);
    expect(server.suggestions()).toHaveLength(1);
    expect(input().placeholder).toBe(defaultPlaceholder);
    expect(screen.queryByText("No suggestion this time.")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
    typeAndSend("Carry on");
    await tick(0);
    expect(server.posts()).toHaveLength(1);
  });

  it("stays fully usable with no loading text and no suggestion when both CLIs fail", async () => {
    // With both small models down the daemon writes no loading phrase and
    // answers suggestion requests with a quiet failure.
    const server = backend([], () => ({
      ok: false,
      status: 502,
      json: async () => ({ error: "No suggestion this time." }),
    }));
    const view = render(panel(settled()));
    typeAndSend("Carry on");
    await tick(0);
    expect(server.turns[0].status).toBe("running");
    expect(server.turns[0].loading_phrase).toBeUndefined();
    expect(screen.getByText("Iris is working")).toBeTruthy();
    // No suggestion is asked for while the reply is being written.
    await tick(SUGGESTION_DELAY * 3);
    expect(server.suggestions()).toHaveLength(0);
    // The owner can keep drafting and queue another message meanwhile.
    typeAndSend("And one more thing");
    await tick(0);
    expect(server.posts()).toHaveLength(2);
    server.turns.forEach((t) => {
      t.status = "completed";
      t.assistant_message_id = `reply-${t.id}`;
    });
    view.rerender(
      panel(
        settled([
          ...server.turns.map((t) => ({
            id: t.user_message_id!,
            role: "user",
            content: t.message,
            created_at: t.created_at,
          })),
          {
            id: "reply-final",
            role: "assistant",
            content: "Done.",
            created_at: new Date(Date.now() + 60_000).toISOString(),
          },
        ]),
      ),
    );
    await tick(3000);
    await tick(SUGGESTION_DELAY);
    expect(server.suggestions()).toHaveLength(1);
    expect(input().placeholder).toBe(defaultPlaceholder);
    expect(screen.queryByRole("status", { name: /suggestions/ })).toBeNull();
    expect(screen.queryByText(/suggestions are off/)).toBeNull();
    fireEvent.change(input(), { target: { value: "Thanks" } });
    expect(input().value).toBe("Thanks");
  });

  it("tells the owner when the approved model is unavailable and stops asking", async () => {
    const server = backend([], () => ({
      ok: false,
      status: 503,
      json: async () => ({
        error:
          "next-message suggestions are off: gpt-6-luna is not offered to the codex login; haiku is not offered to the claude login",
      }),
    }));
    const view = render(panel(settled()));
    await tick(SUGGESTION_DELAY);
    expect(
      screen.getByText(/gpt-6-luna is not offered to the codex login/),
    ).toBeTruthy();
    view.rerender(
      panel(
        settled([
          {
            id: "reply-2",
            role: "assistant",
            content: "One more thing.",
            created_at: "2026-09-16T12:02:00Z",
          },
        ]),
      ),
    );
    await tick(SUGGESTION_DELAY * 3);
    expect(server.suggestions()).toHaveLength(1);
    expect(input().placeholder).toBe(defaultPlaceholder);
  });

  it("treats pending attachments as a draft: no suggestion is asked for or shown, and a dropped file dismisses one", async () => {
    const server = backend([], offering("What should I plant first?"));
    render(panel(settled()));
    const form = input().closest("form")!;
    const dropFile = (name: string) => {
      const files = [new File(["notes"], name, { type: "text/plain" })];
      fireEvent.drop(form, { dataTransfer: { files, types: ["Files"] } });
    };
    // A file dropped before the suggestion is asked for holds it off.
    dropFile("early.txt");
    await tick(SUGGESTION_DELAY * 3);
    expect(server.suggestions()).toHaveLength(0);
    fireEvent.click(
      screen.getByRole("button", { name: "Remove attachment early.txt" }),
    );
    await tick(SUGGESTION_DELAY);
    expect(server.suggestions()).toHaveLength(1);
    expect(input().placeholder).toBe("What should I plant first?");
    // Attaching a file dismisses the suggestion for good, like typing does.
    dropFile("plan.txt");
    await tick(0);
    expect(input().placeholder).toBe(defaultPlaceholder);
    expect(screen.queryByText(/takes the suggestion/)).toBeNull();
    expect(fireEvent.keyDown(input(), { key: "Tab" })).toBe(true);
    expect(input().value).toBe("");
    fireEvent.click(
      screen.getByRole("button", { name: "Remove attachment plan.txt" }),
    );
    await tick(SUGGESTION_DELAY * 3);
    expect(input().placeholder).toBe(defaultPlaceholder);
    expect(server.suggestions()).toHaveLength(1);
  });
});
describe("chat layout", () => {
  it("heads the chat with the assistant's name and its controls", () => {
    backend();
    const onExpand = vi.fn();
    const onClose = vi.fn();
    const view = render(
      <ChatPanel
        state={initial()}
        refresh={vi.fn(async () => {})}
        expanded={false}
        onExpand={onExpand}
        onClose={onClose}
      />,
    );
    const header = screen.getByRole("heading", { level: 2 }).parentElement!;
    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe("Iris");
    // Just the name and three controls: no tagline and no context strip.
    expect(header.children).toHaveLength(4);
    expect(
      screen.getByRole("button", { name: "Past conversations" }),
    ).toBeTruthy();
    const widen = screen.getByRole("button", { name: "Widen the chat" });
    expect(widen.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(widen);
    expect(onExpand).toHaveBeenCalledTimes(1);
    const close = screen.getByRole("button", { name: "Close chat" });
    expect(close.classList.contains("mobile-close")).toBe(true);
    fireEvent.click(close);
    expect(onClose).toHaveBeenCalledTimes(1);
    view.rerender(
      <ChatPanel
        state={initial()}
        refresh={vi.fn(async () => {})}
        expanded
        onExpand={onExpand}
        onClose={onClose}
      />,
    );
    expect(
      screen
        .getByRole("button", { name: "Back to the work" })
        .getAttribute("aria-pressed"),
    ).toBe("true");
    expect(screen.queryByRole("button", { name: "Widen the chat" })).toBeNull();
  });

  it("welcomes an empty conversation with starters that fill the composer", () => {
    const server = backend();
    render(panel());
    expect(
      screen.getByText(
        "Ask about your projects, or hand Iris something to do.",
      ),
    ).toBeTruthy();
    const starters = screen
      .getByText("Ask about your projects, or hand Iris something to do.")
      .parentElement!.querySelectorAll("button");
    expect(Array.from(starters).map((b) => b.textContent)).toEqual([
      "What needs me today?",
      "Start a new project",
      "What do you remember about me?",
    ]);
    fireEvent.click(
      screen.getByRole("button", { name: "Start a new project" }),
    );
    const input = screen.getByLabelText("Message Iris") as HTMLTextAreaElement;
    expect(input.value).toBe("Start a new project");
    expect(document.activeElement).toBe(input);
    // A starter fills the draft; it is not sent.
    expect(server.posts()).toHaveLength(0);
    expect(
      screen.queryByText(/One conversation across your projects/),
    ).toBeNull();
  });

  it("shows no welcome once there are messages, and bylines each one", () => {
    backend();
    const state = initial();
    state.messages = [
      {
        id: "u1",
        role: "user",
        content: "Hello",
        created_at: "2026-09-24T09:00:00Z",
      },
      {
        id: "s1",
        role: "system",
        content: "Model changed",
        created_at: "2026-09-24T09:00:01Z",
      },
      {
        id: "a1",
        role: "assistant",
        content: "Hi there",
        created_at: "2026-09-24T09:00:02Z",
      },
    ];
    render(panel(state));
    expect(screen.queryByText(/Ask about your projects/)).toBeNull();
    const log = screen.getByRole("log");
    const articles = Array.from(log.querySelectorAll("article"));
    expect(articles.map((a) => a.className)).toEqual([
      "message from-you",
      "message from-assistant",
      "message from-assistant",
    ]);
    expect(
      articles.map((a) => a.querySelector(".message-by span")?.textContent),
    ).toEqual(["You", "Iris", "Iris"]);
    // The key hint is for a first message only.
    expect(document.querySelector(".composer-hint")?.textContent).toBe("");
  });

  it("describes the composer's keys when no suggestion shows", () => {
    backend();
    render(panel());
    const hint = document.querySelector(".composer-hint")!;
    expect(hint.textContent).toBe(
      "Enter sends · Shift Enter new line · drop text files to attach",
    );
    expect(
      Array.from(hint.querySelectorAll(".kbd")).map((k) => k.textContent),
    ).toEqual(["Enter", "Shift Enter"]);
    const input = screen.getByLabelText("Message Iris") as HTMLTextAreaElement;
    expect(input.placeholder).toBe("Message Iris");
    expect(
      (screen.getByRole("button", { name: "Send" }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);
  });

  it("shows the model's status while a reply runs, and when it will try again", async () => {
    const retryAt = "2026-09-16T12:05:00Z";
    const server = backend([
      savedTurn({
        model_status: "Waiting for the model",
        loading_phrase: "Gathering the threads…",
        retry_at: retryAt,
      }),
    ]);
    render(panel());
    await tick(0);
    expect(screen.getByText("Waiting for the model").textContent).toBe(
      `Waiting for the model · retrying at ${fullDateLabel(retryAt)}`,
    );
    expect(screen.queryByText("Gathering the threads…")).toBeNull();
    // A running turn needs no delivery line.
    expect(document.querySelector(".turn-delivery")).toBeNull();
    server.turns[0] = {
      ...server.turns[0],
      model_status: undefined,
      retry_at: undefined,
    };
    await tick();
    expect(screen.getByText("Gathering the threads…")).toBeTruthy();
    expect(screen.queryByText(/retrying at/)).toBeNull();
  });

  it("says when live updates fail, and clears it once they return", async () => {
    const server = backend();
    const original = server.fetch.getMockImplementation()!;
    let offline = true;
    server.fetch.mockImplementation(async (path, options) => {
      if (path === "/api/chat/turns" && offline) throw new Error("offline");
      return original(path, options);
    });
    render(panel());
    await tick(0);
    expect(
      screen.getByText("Can't get live updates (offline). Trying again…"),
    ).toBeTruthy();
    offline = false;
    await tick();
    expect(screen.queryByText(/Can't get live updates/)).toBeNull();
  });
});
describe("wake-ups", () => {
  const note = "[A scheduled wake-up. Decide what to do.]\n";
  it("lists what each wake-up saw happen, and nothing meant for the model", () => {
    expect(
      wakeHappenings(
        `${note}wake-1 — check the build\nwhat happened: the build passed\nwake-2 — chase the review\nwhat happened: a PR was merged`,
      ),
    ).toEqual(["the build passed", "a PR was merged"]);
    expect(wakeHappenings(`${note}wake-1 — check the build`)).toEqual([]);
  });
  it("summarises what happened, or says it checked in", () => {
    expect(
      wakeSummary(
        `${note}wake-1 — check the build\nwhat happened: the build passed\nwhat happened: a PR was merged`,
      ),
    ).toBe("the build passed · a PR was merged");
    // The wake's own line is written for the model, so it is not used.
    expect(
      wakeSummary(`${note}wake-1 — check the build\nwake-2 — chase the review`),
    ).toBe("Checked in");
    expect(wakeSummary(`${note}Nothing was due.`)).toBe("Checked in");
  });
  it("shows a wake-up message as its summary and time, with no report", () => {
    backend();
    const state = initial();
    const at = "2026-09-24T09:18:23Z";
    state.messages = [
      {
        id: "w1",
        role: "user",
        origin: "wake",
        content: `${note}wake-1 — check the build\nwhat happened: the build passed`,
        created_at: at,
      },
    ];
    render(panel(state));
    const log = screen.getByRole("log");
    const head = log.querySelector<HTMLElement>("div.wake > p.wake-head")!;
    expect(within(head).getByText("Wake-up")).toBeTruthy();
    expect(within(head).getByText("the build passed")).toBeTruthy();
    expect(head.querySelector("time")?.getAttribute("dateTime")).toBe(at);
    expect(log.textContent).not.toContain("check the build");
    expect(log.textContent).not.toContain("Decide what to do");
    expect(log.querySelector("pre")).toBeNull();
  });
});
describe("slash commands and past conversations", () => {
  const input = () => screen.getByRole("textbox") as HTMLTextAreaElement;
  const log = () => screen.getByRole("log", { hidden: true });
  const past = {
    id: "conv-1",
    title: "Plan the garden",
    started_at: "2026-09-16T12:00:00Z",
    archived_at: "2026-09-16T13:00:00Z",
  };
  // A daemon that knows the commands and keeps past conversations.
  function daemon(suggestion = "") {
    const turns: ChatTurn[] = [];
    let conversation = "current";
    const fetch = vi.fn(async (path: string, options?: RequestInit) => {
      const body = options?.body ? JSON.parse(options.body as string) : {};
      if (path === "/api/chat/turns")
        return result({ turns: turns.map((t) => ({ ...t })), conversation });
      if (path === "/api/chat/suggestion")
        return result({ after: body.after, suggestion });
      if (path === "/api/chat/messages") {
        const command = /^\/(compact|new|clear)$/.exec(body.message)?.[1];
        if (body.message.startsWith("/") && !command)
          return {
            ok: false,
            status: 400,
            json: async () => ({
              error: `${body.message} isn't a command. Try /compact to summarize the conversation, or /new or /clear to start a fresh one.`,
            }),
          };
        const turn: ChatTurn = {
          ...body,
          status: "running",
          created_at: new Date().toISOString(),
          revision: 0,
          events: [],
          ...(command ? { command } : { user_message_id: `user-${body.id}` }),
        };
        turns.push(turn);
        return result(turn);
      }
      if (path === "/api/chat/conversations")
        return result({ conversations: [{ ...past, messages: 2 }] });
      if (path === "/api/chat/conversations/conv-1")
        return result({
          ...past,
          messages: [
            { id: "p1", role: "user", content: "Plan the garden" },
            {
              id: "p2",
              role: "assistant",
              content: "Here is a planting plan.",
            },
          ],
        });
      if (path === "/api/chat/conversations/conv-1/resume") {
        conversation = "conv-1";
        return result({ resumed: true });
      }
      throw new Error(`Unexpected request ${path}`);
    });
    vi.stubGlobal("fetch", fetch);
    return {
      turns,
      fetch,
      replace: (id: string) => {
        conversation = id;
        turns.length = 0;
      },
      calls: (path: string) => fetch.mock.calls.filter(([p]) => p === path),
      sent: () =>
        fetch.mock.calls
          .filter(([p]) => p === "/api/chat/messages")
          .map(([, o]) => JSON.parse(o!.body as string).message),
    };
  }

  it("recognises a command only as the whole message", () => {
    expect(commandIn("/compact")).toBe("compact");
    expect(commandIn("  /NEW\n")).toBe("new");
    expect(commandIn("/clear")).toBe("clear");
    expect(commandIn("/new plan for the garden")).toBeUndefined();
    expect(commandIn("please /compact")).toBeUndefined();
    expect(commandIn("/usr/bin is where it lives")).toBeUndefined();
    // A turn read back without its text is simply not a command.
    expect(commandIn(undefined)).toBeUndefined();
  });

  it("runs /compact as a command, never as something said, and shows the summary", async () => {
    const server = daemon();
    const state = initial();
    const view = render(panel(state));
    typeAndSend("/compact");
    await tick(0);
    expect(server.sent()).toEqual(["/compact"]);
    const row = log().querySelector<HTMLElement>(".chat-command")!;
    expect(within(row).getByText("/compact")).toBeTruthy();
    expect(
      within(row).getByText("Summarize the conversation so far"),
    ).toBeTruthy();
    expect(within(log()).queryByText("You")).toBeNull();
    // The daemon finishes it: the summary joins the conversation.
    Object.assign(server.turns[0], {
      status: "completed",
      outcome: "Summarized 8 earlier messages.",
      assistant_message_id: "summary-1",
    });
    state.messages = [
      {
        id: "summary-1",
        role: "summary",
        content: "The owner is planning a fictional garden.",
        created_at: new Date(Date.now() + 1000).toISOString(),
      },
    ];
    await tick(3000);
    view.rerender(panel(state));
    expect(
      within(row).getByText("Summarized 8 earlier messages."),
    ).toBeTruthy();
    const summary = log().querySelector("details.chat-summary")!;
    expect(summary.hasAttribute("open")).toBe(true);
    expect(summary.textContent).toContain("Summary of the conversation so far");
    expect(summary.textContent).toContain(
      "The owner is planning a fictional garden.",
    );
  });

  it("says a command the assistant asked for was its own", async () => {
    const server = daemon();
    server.turns.push(
      savedTurn({
        id: "asked",
        message: "/new",
        command: "new",
        origin: "assistant",
        user_message_id: undefined,
      }),
    );
    render(panel());
    await tick(0);
    expect(
      within(log()).getByText("Start a fresh conversation · asked by Iris"),
    ).toBeTruthy();
  });

  it("tells the owner an unknown command isn't one, and gives it back to edit", async () => {
    const server = daemon();
    render(panel());
    typeAndSend("/nwe");
    await tick(0);
    expect(server.sent()).toEqual(["/nwe"]);
    expect(screen.getByText("Not sent")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain(
      "/nwe isn't a command. Try /compact",
    );
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    expect(input().value).toBe("/nwe");
  });

  it("opens a fresh conversation with its overview and none of the old turns", async () => {
    const server = daemon();
    server.turns.push(
      savedTurn({ status: "completed", message: "An old topic" }),
    );
    const state = initial();
    state.messages = [
      { id: "user-1", role: "user", content: "An old topic" },
      { id: "reply-1", role: "assistant", content: "An old reply" },
    ];
    const view = render(panel(state));
    await tick(0);
    expect(log().textContent).toContain("An old topic");
    typeAndSend("/new");
    await tick(0);
    expect(server.sent()).toEqual(["/new"]);
    // The daemon archives the old conversation and opens a fresh one.
    server.replace("fresh");
    state.messages = [
      {
        id: "overview",
        role: "assistant",
        origin: "overview",
        content: "Fresh start. The previous conversation is saved in History.",
      },
    ];
    view.rerender(panel(state));
    await tick(3000);
    expect(log().textContent).toContain("Fresh start.");
    expect(log().textContent).not.toContain("An old topic");
    expect(log().textContent).not.toContain("/new");
  });

  it("drops the old conversation's turns even when a poll straddling the reset misnamed them", async () => {
    // What the daemon answers each time it is polled, in order.
    const old = savedTurn({ status: "completed", message: "An old topic" });
    const answers = [
      { turns: [old], conversation: "old" },
      // Read across the reset: the old turn, under the new conversation.
      { turns: [old], conversation: "fresh" },
      { turns: [], conversation: "fresh" },
    ];
    const fetch = vi.fn(async (path: string) => {
      if (path === "/api/chat/turns")
        return result(answers.length > 1 ? answers.shift() : answers[0]);
      if (path === "/api/chat/suggestion") return result({ suggestion: "" });
      throw new Error(`Unexpected request ${path}`);
    });
    vi.stubGlobal("fetch", fetch);
    const state = initial();
    const view = render(panel(state));
    await tick(0);
    expect(log().textContent).toContain("An old topic");
    state.messages = [
      {
        id: "overview",
        role: "assistant",
        origin: "overview",
        content: "Fresh start.",
      },
    ];
    view.rerender(panel(state));
    await tick(3000);
    await tick(3000);
    await tick(3000);
    expect(log().textContent).toContain("Fresh start.");
    expect(log().textContent).not.toContain("An old topic");
  });

  it("keeps a message accepted after a poll was sent that the poll's answer predates", async () => {
    let answer!: (reply: Reply) => void;
    let polls = 0;
    const fetch = vi.fn(async (path: string, options?: RequestInit) => {
      if (path === "/api/chat/turns") {
        polls++;
        if (polls === 1)
          return new Promise<Reply>((resolve) => (answer = resolve));
        return result({ turns: [], conversation: "c" });
      }
      if (path === "/api/chat/messages") {
        const body = JSON.parse(options!.body as string);
        return result({
          ...body,
          status: "queued",
          created_at: new Date().toISOString(),
          revision: 0,
          events: [],
        });
      }
      throw new Error(`Unexpected request ${path}`);
    });
    vi.stubGlobal("fetch", fetch);
    render(panel());
    await tick(0);
    typeAndSend("Just sent");
    await tick(0);
    // The first poll was asked before the message arrived; it cannot list it.
    answer(result({ turns: [], conversation: "c" }));
    await tick(0);
    const queue = screen.getByRole("region", { name: "Queued messages" });
    expect(within(queue).getByText("Just sent")).toBeTruthy();
    // A poll asked afterwards that leaves it out means it has gone.
    await tick(3000);
    expect(
      screen.queryByRole("region", { name: "Queued messages" }),
    ).toBeNull();
  });

  it("keeps attachments and sends nothing when a command comes with them", async () => {
    const server = daemon();
    render(panel());
    const form = input().closest("form")!;
    const files = [new File(["notes"], "notes.txt", { type: "text/plain" })];
    fireEvent.drop(form, { dataTransfer: { files, types: ["Files"] } });
    await tick(0);
    typeAndSend("/clear");
    await tick(0);
    expect(server.sent()).toEqual([]);
    expect(screen.getByRole("alert").textContent).toBe(
      "Send /clear on its own. Your attachments stay here for your next message.",
    );
    const list = screen.getByRole("list", { name: "Attachments" });
    expect(within(list).getByText("notes.txt")).toBeTruthy();
    expect(input().value).toBe("/clear");
    // Without the command the attachment goes as usual.
    fireEvent.change(input(), { target: { value: "Here are my notes" } });
    fireEvent.keyDown(input(), { key: "Enter" });
    await tick(0);
    expect(server.sent()).toEqual([
      "Here are my notes\n\nAttached file: notes.txt\n```\nnotes\n```",
    ]);
  });

  it("typing a command dismisses a suggestion, and Tab is left alone", async () => {
    const server = daemon("What should I plant first?");
    const state = initial();
    state.messages = [
      { id: "user-1", role: "user", content: "Plan the garden" },
      { id: "reply-1", role: "assistant", content: "Here is a plan." },
    ];
    render(panel(state));
    await tick(SUGGESTION_DELAY);
    expect(input().placeholder).toBe("What should I plant first?");
    fireEvent.change(input(), { target: { value: "/" } });
    expect(input().placeholder).toBe("Message Iris");
    expect(fireEvent.keyDown(input(), { key: "Tab" })).toBe(true);
    expect(input().value).toBe("/");
    fireEvent.change(input(), { target: { value: "/compact" } });
    fireEvent.keyDown(input(), { key: "Enter" });
    await tick(0);
    expect(server.sent()).toEqual(["/compact"]);
    expect(server.calls("/api/chat/suggestion")).toHaveLength(1);
  });

  it("suggests after a fresh start's overview, but not over a summary", async () => {
    const server = daemon("What needs me today?");
    const state = initial();
    state.messages = [
      {
        id: "summary-1",
        role: "summary",
        content: "The owner is planning a garden.",
      },
    ];
    const view = render(panel(state));
    await tick(SUGGESTION_DELAY * 2);
    expect(server.calls("/api/chat/suggestion")).toHaveLength(0);
    state.messages = [
      {
        id: "overview",
        role: "assistant",
        origin: "overview",
        content: "Fresh start.",
      },
    ];
    view.rerender(panel(state));
    await tick(SUGGESTION_DELAY);
    const asked = server.calls("/api/chat/suggestion");
    expect(asked).toHaveLength(1);
    expect(JSON.parse(asked[0][1]!.body as string)).toEqual({
      after: "overview",
    });
    expect(input().placeholder).toBe("What needs me today?");
  });

  it("lists past conversations, opens one, and switches back to continue it", async () => {
    const server = daemon();
    const refresh = vi.fn(async () => {});
    render(panel(initial(), refresh));
    fireEvent.click(screen.getByRole("button", { name: "Past conversations" }));
    await tick(0);
    const history = screen.getByRole("region", { name: "Past conversations" });
    expect(log().hidden).toBe(true);
    fireEvent.click(within(history).getByText("Plan the garden"));
    await tick(0);
    expect(within(history).getByText("Here is a planting plan.")).toBeTruthy();
    fireEvent.click(
      within(history).getByRole("button", {
        name: "Continue this conversation",
      }),
    );
    await tick(0);
    expect(server.calls("/api/chat/conversations/conv-1/resume")).toHaveLength(
      1,
    );
    expect(refresh).toHaveBeenCalled();
    expect(
      screen.queryByRole("region", { name: "Past conversations" }),
    ).toBeNull();
    expect(log().hidden).toBe(false);
    // Once it is picked up, the composer writes to it.
    expect(screen.getByRole("textbox")).toBeTruthy();
  });

  it("offers nothing to write in while a past conversation is open, and keeps the draft for the return", async () => {
    const server = daemon();
    render(panel());
    const form = input().closest("form")!;
    const files = [new File(["notes"], "notes.txt", { type: "text/plain" })];
    fireEvent.drop(form, { dataTransfer: { files, types: ["Files"] } });
    await tick(0);
    fireEvent.change(input(), { target: { value: "/clear" } });
    fireEvent.click(screen.getByRole("button", { name: "Past conversations" }));
    await tick(0);
    fireEvent.click(screen.getByText("Plan the garden"));
    await tick(0);
    // Beneath a past conversation there is no composer to send from:
    // anything written would go to the current conversation instead.
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.queryByRole("button", { name: "Send" })).toBeNull();
    expect(screen.queryByRole("list", { name: "Attachments" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "All conversations" }));
    fireEvent.click(screen.getByRole("button", { name: "Back to the chat" }));
    await tick(0);
    expect(server.sent()).toEqual([]);
    expect(input().value).toBe("/clear");
    const list = screen.getByRole("list", { name: "Attachments" });
    expect(within(list).getByText("notes.txt")).toBeTruthy();
  });

  it("says why a conversation can't be picked up yet", async () => {
    const server = daemon();
    const original = server.fetch.getMockImplementation()!;
    server.fetch.mockImplementation(async (path, options) =>
      path.endsWith("/resume")
        ? {
            ok: false,
            status: 409,
            json: async () => ({
              error: "The assistant is replying; switch once it has finished",
            }),
          }
        : original(path, options),
    );
    render(panel());
    fireEvent.click(screen.getByRole("button", { name: "Past conversations" }));
    await tick(0);
    fireEvent.click(screen.getByText("Plan the garden"));
    await tick(0);
    fireEvent.click(
      screen.getByRole("button", { name: "Continue this conversation" }),
    );
    await tick(0);
    expect(screen.getByRole("alert").textContent).toBe(
      "The assistant is replying; switch once it has finished",
    );
    expect(
      screen.getByRole("region", { name: "Past conversations" }),
    ).toBeTruthy();
  });
});
describe("model session", () => {
  const line = () => screen.queryByRole("group", { name: "Model session" });
  const session = (overrides: Partial<ChatSession> = {}): ChatSession => ({
    engine: "claude",
    model: "opus",
    started_at: "2026-09-16T12:00:00Z",
    opened: "fresh",
    updated_at: "2026-09-16T12:05:00Z",
    ...overrides,
  });
  // A daemon whose conversation runs on the session given, if any.
  function daemon(current?: ChatSession) {
    const turns: ChatTurn[] = [];
    const shown = { session: current };
    const fetch = vi.fn(async (path: string, options?: RequestInit) => {
      if (path === "/api/chat/turns")
        return result({
          turns: turns.map((t) => ({ ...t })),
          session: shown.session,
        });
      if (path === "/api/chat/suggestion")
        return result({ after: "", suggestion: "" });
      if (path === "/api/chat/conversations")
        return result({ conversations: [] });
      if (path === "/api/chat/messages") {
        const body = JSON.parse(String(options?.body));
        const turn: ChatTurn = {
          ...body,
          status: "running",
          created_at: new Date().toISOString(),
          revision: 0,
          events: [],
          command: commandIn(body.message),
        };
        turns.push(turn);
        return result(turn);
      }
      throw new Error(`Unexpected request ${path}`);
    });
    vi.stubGlobal("fetch", fetch);
    return {
      turns,
      shown,
      sent: () =>
        fetch.mock.calls
          .filter(([p]) => p === "/api/chat/messages")
          .map(([, o]) => JSON.parse(String(o?.body)).message),
    };
  }

  it("says what the session runs on and how full it is, leaving out what is unknown", async () => {
    const server = daemon(
      session({
        context_used: 84_000,
        context_window: 200_000,
        input: 10_000,
        cached_input: 9_000,
        compactions: 2,
      }),
    );
    render(panel());
    await tick(0);
    expect(line()!.textContent).toContain(
      "Claude · opus · 42% of context · 90% cached · compacted 2×",
    );
    // A fresh session says nothing about how it was opened.
    expect(line()!.textContent).not.toContain("Picked up");
    expect(line()!.textContent).not.toContain("Started afresh");
    server.shown.session = session({
      engine: "codex",
      model: "gpt-5",
      context_used: 1_000,
      cached_input: 0,
    });
    await tick(3000);
    expect(line()!.querySelector(".chat-session-about")!.textContent).toBe(
      "Codex · gpt-5",
    );
  });

  it("says when it picked up where it left off, or had to start afresh", async () => {
    const server = daemon(session({ opened: "resumed" }));
    render(panel());
    await tick(0);
    expect(
      within(line()!).getByText("Picked up where it left off"),
    ).toBeTruthy();
    server.shown.session = session({ opened: "rebuilt" });
    await tick(3000);
    expect(
      within(line()!).getByText(
        "Started afresh: the last session couldn't be resumed",
      ),
    ).toBeTruthy();
    expect(line()!.textContent).not.toContain("Picked up");
  });

  it("shows nothing for a conversation run turn by turn", async () => {
    daemon();
    render(panel());
    await tick(0);
    expect(line()).toBeNull();
    expect(screen.queryByRole("button", { name: "Compact" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Start fresh" })).toBeNull();
  });

  it("compacts or starts fresh as typing the command would, once nothing is running", async () => {
    const server = daemon(session());
    render(panel());
    await tick(0);
    const compact = () =>
      screen.getByRole<HTMLButtonElement>("button", { name: "Compact" });
    const fresh = () =>
      screen.getByRole<HTMLButtonElement>("button", { name: "Start fresh" });
    fireEvent.click(compact());
    await tick(0);
    expect(server.sent()).toEqual(["/compact"]);
    const row = screen
      .getByRole("log")
      .querySelector<HTMLElement>(".chat-command")!;
    expect(within(row).getByText("/compact")).toBeTruthy();
    expect(compact().disabled).toBe(true);
    expect(fresh().disabled).toBe(true);
    Object.assign(server.turns[0], { status: "completed" });
    await tick(3000);
    expect(compact().disabled).toBe(false);
    fireEvent.click(fresh());
    await tick(0);
    expect(server.sent()).toEqual(["/compact", "/new"]);
  });

  it("stays out of the way while a past conversation is open", async () => {
    daemon(session());
    render(panel());
    await tick(0);
    fireEvent.click(screen.getByRole("button", { name: "Past conversations" }));
    await tick(0);
    expect(line()).toBeNull();
  });
});

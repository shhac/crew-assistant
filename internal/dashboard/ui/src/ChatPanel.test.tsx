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
import { ChatPanel } from "./ChatPanel";
import { ConversationMarkdown } from "./ConversationMarkdown";
import { normalizeState, type ChatTurn, type State } from "./api";
beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
const initial = () =>
  normalizeState({ assistant: { name: "Iris", personality: "" } });
function panel(state = initial(), refresh = vi.fn(async () => {})) {
  return (
    <ChatPanel
      state={state}
      refresh={refresh}
      expanded={false}
      onExpand={() => {}}
      onClose={() => {}}
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
function backend(seed: ChatTurn[] = []) {
  const turns = [...seed];
  const fetch = vi.fn(async (path: string, options?: RequestInit) => {
    if (path === "/api/chat/turns")
      return result({ turns: turns.map((t) => ({ ...t })) });
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
    posts: () => fetch.mock.calls.filter(([, o]) => o?.method === "POST"),
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
    expect(screen.getByText("Iris is working through it…")).toBeTruthy();
    typeAndSend("Another thought");
    await tick(0);
    expect(server.posts()).toHaveLength(2);
    // A queued message now waits in the queue, where its position is its
    // status and it can be changed before it runs.
    const queue = screen.getByRole("region", { name: "Queued messages" });
    expect(within(queue).getByText("Another thought")).toBeTruthy();
    expect(within(queue).getByText(/1 message queued/)).toBeTruthy();
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
    expect(screen.getByText("Delivery unconfirmed")).toBeTruthy();
    expect(input.value).toBe("A second thought");
    await tick(6000);
    expect(server.posts()).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Retry delivery" }));
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
        name: "Cancel queued message: Then review it",
      }),
    );
    await tick(0);
    expect(screen.getByText("Cancelled before starting")).toBeTruthy();
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
      name: "Assistant tool activity",
    });
    expect(within(activity).getByText("Creating project")).toBeTruthy();
    expect(within(activity).getByText("In progress")).toBeTruthy();
    expect(screen.getByText("Gathering the threads…")).toBeTruthy();
    server.turns[0] = {
      ...server.turns[0],
      status: "completed",
      assistant_message_id: "answer-1",
      events: [{ ...server.turns[0].events[0], status: "completed" }],
    };
    await tick();
    expect(within(activity).getByText("Completed")).toBeTruthy();
    // A finished step collapses into a count so it stops competing with the
    // reply, while still being one click away.
    // One finished step is shown rather than hidden behind a summary that
    // would cost a row and a click to save a row.
    expect(within(activity).queryByText(/steps completed/)).toBeNull();
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
      screen.getByText("Reply interrupted · not automatically retried"),
    ).toBeTruthy();
    expect(screen.getByText("Failed", { exact: true })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Retry delivery" })).toBeNull();
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
    expect(screen.getByText("Message not sent")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Retry delivery" })).toBeNull();
    fireEvent.click(
      screen.getByRole("button", { name: "Restore message to draft" }),
    );
    expect(input.value).toBe("A next draft\n\nOne more thing");
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
        name: "Cancel queued message: Prepare the project",
      }),
    );
    await tick(0);
    expect(screen.getByText("Cancelled before starting")).toBeTruthy();
    await act(async () => {
      release(result({ turns: [stale] }));
    });
    expect(screen.getByText("Cancelled before starting")).toBeTruthy();
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
    fireEvent.click(screen.getByRole("button", { name: "Retry delivery" }));
    await tick(0);
    expect(screen.getByText("Delivery unconfirmed")).toBeTruthy();
    expect(
      screen.queryByRole("button", { name: "Restore message to draft" }),
    ).toBeNull();
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
    expect(
      screen.getByText("Waiting to send · kept in this browser"),
    ).toBeTruthy();
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
    expect(
      screen.getByText("Waiting to send · kept in this browser"),
    ).toBeTruthy();
    await tick();
    expect(server.posts()).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Retry delivery" }));
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
    expect(screen.getByText("Outcome unconfirmed")).toBeTruthy();
    expect(screen.queryByText("Failed", { exact: true })).toBeNull();
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
    expect(screen.getByText("Delivery unconfirmed")).toBeTruthy();
    expect(
      screen.getByText(
        "Delivery confirmation timed out. Checking whether your message arrived.",
      ),
    ).toBeTruthy();
    expect(
      screen.getByText("Waiting to send · kept in this browser"),
    ).toBeTruthy();
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
    const send = screen.getByRole("button", { name: "Send message" });
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
    expect(screen.getByText("Message not sent")).toBeTruthy();
    expect(attachments()).toBeNull();
    fireEvent.click(
      screen.getByRole("button", { name: "Restore message to draft" }),
    );
    expect(input.value).toBe("Review this");
    expect(within(attachments()!).getByText("draft.md")).toBeTruthy();
  });
});

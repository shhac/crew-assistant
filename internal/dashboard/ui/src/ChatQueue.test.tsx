// @vitest-environment jsdom
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChatQueue, moveItem } from "./ChatQueue";

const turns = [
  { id: "a", message: "Do the first thing", revision: 0 },
  { id: "b", message: "Then the second", revision: 0 },
  { id: "c", message: "Then the third", revision: 0 },
];

function mount(
  calls: { path: string; options?: RequestInit }[] = [],
  reply: (path: string) => { status: number; body?: unknown } = () => ({
    status: 200,
  }),
) {
  const fetch = vi.fn(async (path: string, options?: RequestInit) => {
    calls.push({ path, options });
    const { status, body } = reply(path);
    return { ok: status < 400, status, json: async () => body ?? {} };
  });
  vi.stubGlobal("fetch", fetch);
  const onChanged = vi.fn(async () => {});
  render(<ChatQueue turns={turns} revision={7} onChanged={onChanged} />);
  return { calls, onChanged };
}

const body = (c: { options?: RequestInit }) =>
  JSON.parse(String(c.options?.body ?? "{}"));

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("queued messages", () => {
  it("shows each message with the position it will run in", () => {
    mount();
    const list = screen.getAllByRole("listitem");
    expect(list).toHaveLength(3);
    expect(within(list[0]).getByText("Do the first thing")).toBeTruthy();
    expect(within(list[2]).getByText("Then the third")).toBeTruthy();
  });

  // The queue must be reorderable without a pointer.
  it("moves a message from the keyboard and submits the whole order", async () => {
    const { calls } = mount();
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: "Move message 3 earlier" }),
      );
    });
    const reorder = calls.find((c) => c.path === "/api/chat/queue");
    expect(reorder?.options?.method).toBe("PUT");
    // The whole intended order, against the revision it was decided on.
    expect(body(reorder!)).toEqual({ order: ["a", "c", "b"], revision: 7 });
  });

  it("cannot move the ends off the queue", () => {
    mount();
    expect(
      screen.getByRole("button", { name: "Move message 1 earlier" }),
    ).toHaveProperty("disabled", true);
    expect(
      screen.getByRole("button", { name: "Move message 3 later" }),
    ).toHaveProperty("disabled", true);
  });

  // Opening an editor holds the queue, so nothing behind it can overtake.
  it("holds the queue while editing and releases it afterwards", async () => {
    const { calls } = mount();
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Edit message 2" }));
    });
    const held = calls.find(
      (c) =>
        c.path === "/api/chat/messages/b/hold" && c.options?.method === "POST",
    );
    expect(held).toBeTruthy();
    expect(body(held!)).toEqual({ reason: "editing" });

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    });
    expect(
      calls.some(
        (c) =>
          c.path === "/api/chat/messages/b/hold" &&
          c.options?.method === "DELETE",
      ),
    ).toBe(true);
  });

  it("saves an edit against the revision it was opened on", async () => {
    const { calls } = mount();
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Edit message 1" }));
    });
    fireEvent.change(screen.getByLabelText("Edit message"), {
      target: { value: "Do the first thing, carefully" },
    });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Save" }));
    });
    const edit = calls.find(
      (c) => c.path === "/api/chat/messages/a" && c.options?.method === "PATCH",
    );
    expect(body(edit!)).toEqual({
      message: "Do the first thing, carefully",
      revision: 0,
    });
  });

  it("says the queue is paused while a change is open", () => {
    render(
      <ChatQueue
        turns={turns}
        revision={7}
        hold={{ turn_id: "b", reason: "editing", expires_at: "" }}
        onChanged={vi.fn()}
      />,
    );
    expect(screen.getByRole("status").textContent).toBe(
      "Paused while you change it. It picks up again on its own.",
    );
  });

  it("labels the queue with how many messages are up next", () => {
    mount();
    const queue = screen.getByRole("region", { name: "Queued messages" });
    expect(within(queue).getByText("Up next (3)")).toBeTruthy();
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("removes a queued message through its owner", () => {
    vi.stubGlobal("fetch", vi.fn());
    const onCancel = vi.fn();
    render(
      <ChatQueue
        turns={turns}
        revision={7}
        cancelling={new Set(["c"])}
        onCancel={onCancel}
        onChanged={vi.fn()}
      />,
    );
    fireEvent.click(
      screen.getByRole("button", {
        name: "Remove queued message: Then the second",
      }),
    );
    expect(onCancel).toHaveBeenCalledWith("b");
    // A removal already under way cannot be asked for twice.
    expect(
      screen.getByRole("button", {
        name: "Remove queued message: Then the third",
      }),
    ).toHaveProperty("disabled", true);
  });

  it("invites more writing while a reply runs and nothing is queued", () => {
    render(<ChatQueue turns={[]} revision={0} running onChanged={vi.fn()} />);
    expect(
      screen.getByText(
        "Keep writing if you like. Each message gets its own reply.",
      ),
    ).toBeTruthy();
    expect(
      screen.queryByRole("region", { name: "Queued messages" }),
    ).toBeNull();
  });

  it("renders nothing when no message is waiting", () => {
    const { container } = render(
      <ChatQueue turns={[]} revision={0} onChanged={vi.fn()} />,
    );
    expect(container.innerHTML).toBe("");
  });

  // The queue polls, so this component re-renders with a fresh turns array
  // every second or so. The hold must not be released and retaken on each one:
  // that would leave a window where the message being edited could start.
  it("keeps one hold across re-renders while editing", async () => {
    const calls: { path: string; options?: RequestInit }[] = [];
    const fetch = vi.fn(async (path: string, options?: RequestInit) => {
      calls.push({ path, options });
      return { ok: true, status: 200, json: async () => ({}) };
    });
    vi.stubGlobal("fetch", fetch);
    const { rerender } = render(
      <ChatQueue turns={turns} revision={7} onChanged={vi.fn()} />,
    );
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Edit message 2" }));
    });
    const holds = () =>
      calls.filter(
        (c) =>
          c.path === "/api/chat/messages/b/hold" &&
          c.options?.method === "POST",
      ).length;
    const releases = () =>
      calls.filter(
        (c) =>
          c.path === "/api/chat/messages/b/hold" &&
          c.options?.method === "DELETE",
      ).length;
    expect(holds()).toBe(1);

    // Three polls' worth of fresh arrays carrying identical data.
    for (let i = 0; i < 3; i++) {
      await act(async () => {
        rerender(
          <ChatQueue
            turns={turns.map((t) => ({ ...t }))}
            revision={7}
            onChanged={vi.fn()}
          />,
        );
      });
    }
    expect(releases()).toBe(0);
    expect(holds()).toBe(1);
  });

  // The 409 is the whole point of the revision scheme: a turn started while the
  // owner was deciding. The displayed order must go back to the daemon's.
  it("puts the queue back and says why when a reorder is refused", async () => {
    mount([], (path) =>
      path === "/api/chat/queue"
        ? { status: 409, body: { error: "the queue changed since you saw it" } }
        : { status: 200 },
    );
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: "Move message 3 earlier" }),
      );
    });
    expect(screen.getByRole("alert").textContent).toContain(
      "the queue changed since you saw it",
    );
    const list = screen.getAllByRole("listitem");
    expect(
      list.map((li) => li.querySelector(".queue-text")?.textContent ?? ""),
    ).toEqual(["Do the first thing", "Then the second", "Then the third"]);
  });

  it("tells the owner when the queue could not be held", async () => {
    mount([], (path) =>
      path.endsWith("/hold")
        ? { status: 409, body: { error: "another message is being changed" } }
        : { status: 200 },
    );
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Edit message 1" }));
    });
    expect(screen.getByRole("alert").textContent).toContain(
      "another message is being changed",
    );
  });

  // The reorder maths, without a drag gesture to simulate.
  it("moves one entry and leaves an impossible move alone", () => {
    const ids = ["a", "b", "c"];
    expect(moveItem(ids, 2, 0)).toEqual(["c", "a", "b"]);
    expect(moveItem(ids, 0, 2)).toEqual(["b", "c", "a"]);
    expect(moveItem(ids, 1, 1)).toBe(ids);
    for (const [from, to] of [
      [0, -1],
      [-1, 0],
      [0, 3],
      [3, 0],
    ]) {
      expect(moveItem(ids, from, to)).toBe(ids);
    }
    // The input is never mutated.
    expect(ids).toEqual(["a", "b", "c"]);
  });
});

// @vitest-environment jsdom
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { PMChat } from "./PMChat";
import { ProjectPage } from "./ProjectPage";
import { Composer } from "./Composer";
import { normalizeState, type Project, type PMChatMessage } from "./api";
import { href, parseRoute } from "./router";
import { svgURL } from "./Avatar";

const project: Project = {
  id: "p",
  title: "Website",
  status: "active",
  brief: { version: 1, goal: "Update it", criteria: [] },
  playbook: {
    template: "draft",
    medium: "documents",
    roles: [{ name: "Pia", kinds: ["pm"], engine: "claude" }],
    max_rounds: 3,
    deliver: "owner",
  },
};
const state = normalizeState({ projects: [project] });
const at = "2026-10-01T10:00:00Z";
const presetSVG =
  '<svg xmlns="http://www.w3.org/2000/svg"><circle r="20"/></svg>';
let messages: PMChatMessage[];
let calls: { path: string; method: string; body?: unknown }[];
beforeEach(() => {
  messages = [];
  calls = [];
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", {
    configurable: true,
    value() {
      this.setAttribute("open", "");
    },
  });
  Object.defineProperty(HTMLDialogElement.prototype, "close", {
    configurable: true,
    value() {
      this.removeAttribute("open");
    },
  });
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, options: RequestInit = {}) => {
      const method = options.method || "GET";
      const body = options.body ? JSON.parse(String(options.body)) : undefined;
      calls.push({ path, method, body });
      let result: unknown = { messages: [...messages], avatar_svg: presetSVG };
      if (method === "POST" && path.endsWith("/messages")) {
        const m: PMChatMessage = {
          id: body.id,
          project_id: "p",
          from: "owner",
          text: body.text,
          status: "waiting",
          at,
        };
        messages.push(m);
        result = m;
      }
      if (method === "POST" && path.endsWith("/retry")) {
        messages = messages.map((m) => ({ ...m, status: "waiting" }));
        result = messages[0];
      }
      return new Response(JSON.stringify(result), {
        status: method === "POST" ? 201 : 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
function Chat() {
  const [draft, setDraft] = useState("");
  return (
    <PMChat project={project} state={state} draft={draft} onDraft={setDraft} />
  );
}
function owner(status: PMChatMessage["status"] = "waiting"): PMChatMessage {
  return {
    id: "msg",
    project_id: "p",
    from: "owner",
    text: "What comes first?",
    status,
    at,
  };
}
describe("project PM chat", () => {
  it("repeats the memberless preset and immediately switches to the current member's face", async () => {
    messages = [{ ...owner("answered"), from: "pm", by: "Pia", text: "Reply" }];
    const view = render(<PMChat project={project} state={state} />);
    await screen.findByText("Reply");
    const faces = () =>
      view.container.querySelectorAll<HTMLImageElement>("img.avatar");
    await waitFor(() => expect(faces()).toHaveLength(2));
    for (const face of faces()) {
      expect(face.getAttribute("src")).toBe(svgURL(presetSVG));
      expect(face.alt).toBe("");
    }
    expect([...faces()].map((face) => face.width)).toEqual([40, 24]);
    const filled = {
      ...project,
      playbook: {
        ...project.playbook!,
        roles: [{ ...project.playbook!.roles[0], member: "pia" }],
      },
    };
    const withMember = normalizeState({
      projects: [filled],
      members: [
        {
          id: "pia",
          name: "Pia",
          kinds: ["pm"],
          engine: "claude",
          learnings: [],
          avatar: { image: "a".repeat(32) },
        },
      ],
    });
    view.rerender(<PMChat project={filled} state={withMember} />);
    expect(faces()).toHaveLength(2);
    for (const face of faces())
      expect(face.getAttribute("src")).toContain(
        `/api/avatars/${"a".repeat(32)}/`,
      );
    view.rerender(<PMChat project={project} state={state} />);
    for (const face of faces())
      expect(face.getAttribute("src")).toBe(svgURL(presetSVG));
    expect(calls.filter((c) => c.method !== "GET")).toHaveLength(0);
  });

  it("round-trips the PM URL and offers the tab only with a PM", async () => {
    expect(href(parseRoute("#/projects/x/pm"))).toBe("#/projects/x/pm");
    const { rerender } = render(
      <ProjectPage
        project={project}
        route={{ page: "project", id: "p", tab: "pm" }}
        state={state}
        refresh={async () => {}}
      />,
    );
    expect(
      screen.getByRole("link", { name: "PM chat" }).getAttribute("href"),
    ).toBe("#/projects/p/pm");
    expect(screen.getByText("PM · Website")).toBeTruthy();
    rerender(
      <ProjectPage
        project={{ ...project, playbook: { ...project.playbook!, roles: [] } }}
        route={{ page: "project", id: "p", tab: "pm" }}
        state={state}
        refresh={async () => {}}
      />,
    );
    expect(screen.queryByRole("link", { name: "PM chat" })).toBeNull();
    expect(screen.getByText(/This project has no PM yet/)).toBeTruthy();
    expect(
      screen.getByRole("link", { name: "Open Team" }).getAttribute("href"),
    ).toBe("#/projects/p/team");
  });

  it("offers the no-PM callout on Team without a dead chat tab", () => {
    render(
      <ProjectPage
        project={{ ...project, playbook: { ...project.playbook!, roles: [] } }}
        route={{ page: "project", id: "p", tab: "team" }}
        state={state}
        refresh={async () => {}}
      />,
    );
    expect(screen.queryByRole("link", { name: "PM chat" })).toBeNull();
    expect(screen.getByText(/This project has no PM yet/)).toBeTruthy();
    expect(
      screen.getByRole("link", { name: "Open Team" }).getAttribute("href"),
    ).toBe("#/projects/p/team");
  });
  it("sends with Enter, shows waiting then a polled reply and receipt links", async () => {
    render(<Chat />);
    await waitFor(() => expect(calls.length).toBeGreaterThan(0));
    const field = screen.getByRole("textbox") as HTMLTextAreaElement;
    fireEvent.change(field, { target: { value: "What comes first?" } });
    fireEvent.keyDown(field, { key: "Enter", shiftKey: true });
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fireEvent.keyDown(field, { key: "Enter" });
    await screen.findByText("Waiting for Pia…");
    expect(screen.getByText("What comes first?")).toBeTruthy();
    expect(field.value).toBe("");
    messages = [
      { ...messages[0], status: "answered" },
      {
        id: "reply",
        project_id: "p",
        from: "pm",
        by: "Pia",
        text: "Checkout first.",
        status: "answered",
        reply_to: messages[0].id,
        at,
        changes: [
          {
            kind: "linked",
            summary: "Notes now wait for checkout",
            tasks: ["notes", "checkout"],
          },
        ],
      },
    ];
    await screen.findByText("Pia · PM", {}, { timeout: 4000 });
    expect(screen.getByText("Checkout first.")).toBeTruthy();
    expect(screen.getByText("Changes made")).toBeTruthy();
    expect(
      screen
        .getAllByRole("link", { name: "Open request" })[0]
        .getAttribute("href"),
    ).toBe("#/projects/p/requests/notes");
    expect(
      screen.getByRole("link", { name: "View on board" }).getAttribute("href"),
    ).toBe("#/projects/p");
  });
  it("retries a failure once without changing the separate draft", async () => {
    messages = [
      {
        ...owner("failed"),
        error: "Stopped before Pia replied",
        changes: [
          { kind: "updated", summary: "Updated notes", tasks: ["notes"] },
        ],
      },
    ];
    render(<Chat />);
    await screen.findByText("Pia couldn’t reply. Your message is still here.");
    const field = screen.getByRole("textbox") as HTMLTextAreaElement;
    fireEvent.change(field, { target: { value: "Unsent draft" } });
    const retry = screen.getByRole("button", { name: "Retry" });
    fireEvent.click(retry);
    fireEvent.click(retry);
    await screen.findByText("Waiting for Pia…");
    expect(field.value).toBe("Unsent draft");
    expect(calls.filter((c) => c.path.endsWith("/retry"))).toHaveLength(1);
    expect(screen.getByText("Changes made")).toBeTruthy();
  });
  it("keeps scroll and draft on refresh, and reloads the retained log on return", async () => {
    messages = [
      owner("answered"),
      {
        id: "reply",
        project_id: "p",
        from: "pm",
        text: "Earlier reply",
        by: "Pia",
        status: "answered",
        at,
      },
    ];
    const view = render(<Chat />);
    await screen.findByText("Earlier reply");
    const field = screen.getByRole("textbox") as HTMLTextAreaElement;
    fireEvent.change(field, { target: { value: "Draft" } });
    const log = screen.getByRole("log");
    log.scrollTop = 120;
    view.rerender(<Chat />);
    expect(log.scrollTop).toBe(120);
    expect(field.value).toBe("Draft");
    view.unmount();
    render(<Chat />);
    await screen.findByText("Earlier reply");
  });

  it("scrolls on load, send and status changes but leaves unchanged polls alone", async () => {
    vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(900);
    messages = [owner()];
    render(<Chat />);
    await screen.findByText("What comes first?");
    const log = screen.getByRole("log");
    await waitFor(() => expect(log.scrollTop).toBe(900));
    const field = screen.getByRole("textbox") as HTMLTextAreaElement;
    fireEvent.change(field, { target: { value: "Unsent draft" } });
    log.scrollTop = 120;
    const gets = calls.filter((c) => c.method === "GET").length;
    await waitFor(
      () =>
        expect(calls.filter((c) => c.method === "GET").length).toBeGreaterThan(
          gets,
        ),
      { timeout: 4000 },
    );
    expect(log.scrollTop).toBe(120);
    expect(field.value).toBe("Unsent draft");
    messages = [{ ...messages[0], status: "working", by: "Original PM" }];
    await screen.findByText(
      /Original PM is looking at the project/,
      {},
      { timeout: 4000 },
    );
    await waitFor(() => expect(log.scrollTop).toBe(900));
    log.scrollTop = 120;
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() =>
      expect(within(log).getByText("Unsent draft")).toBeTruthy(),
    );
    await waitFor(() => expect(log.scrollTop).toBe(900));
    messages = [
      { ...messages[0], status: "answered" },
      { ...messages[1], status: "answered" },
      {
        id: "reply",
        project_id: "p",
        from: "pm",
        text: "New reply",
        status: "answered",
        by: "Original PM",
        at,
      },
    ];
    log.scrollTop = 120;
    await screen.findByText("New reply", {}, { timeout: 4000 });
    await waitFor(() => expect(log.scrollTop).toBe(900));
  }, 12000);
  it("links request names within receipt summaries without repeating titles", async () => {
    const receiptState = normalizeState({
      projects: [project],
      tasks: [
        {
          id: "notes",
          project_id: "p",
          objective: "Notes",
          status: "queued",
          criteria: [],
          stage: "todo",
          round: 1,
          revisions: [],
          verdicts: [],
        },
        {
          id: "checkout",
          project_id: "p",
          objective: "Checkout",
          status: "queued",
          criteria: [],
          stage: "todo",
          round: 1,
          revisions: [],
          verdicts: [],
        },
      ],
    });
    messages = [
      {
        id: "reply",
        project_id: "p",
        from: "pm",
        by: "Pia",
        text: "Done.",
        status: "answered",
        at,
        changes: [
          {
            kind: "linked",
            summary: "Notes now waits for Checkout",
            tasks: ["notes", "checkout"],
          },
          { kind: "queued", summary: "Queued “Notes”", tasks: ["notes"] },
          {
            kind: "updated",
            summary: "Updated “Checkout”",
            tasks: ["checkout"],
          },
        ],
      },
    ];
    render(<PMChat project={project} state={receiptState} />);
    const receipt = await screen.findByRole("region", { name: "Changes made" });
    expect(receipt.textContent).toBe(
      "Changes madeUpdated “Checkout”Notes now waits for CheckoutQueued “Notes”View on board",
    );
    expect(
      within(receipt).getAllByRole("link", { name: "Notes" }),
    ).toHaveLength(2);
    expect(
      within(receipt).getAllByRole("link", { name: "Checkout" }),
    ).toHaveLength(2);
  });
  it("uses the recorded PM name for pending and failed turns after a seat rename", async () => {
    messages = [
      { ...owner("waiting"), by: "Original PM" },
      { ...owner("working"), id: "working", by: "Original PM" },
      { ...owner("failed"), id: "failed", by: "Original PM" },
    ];
    render(<Chat />);
    await screen.findByText("Waiting for Original PM…");
    expect(
      screen.getByText(/Original PM is looking at the project/),
    ).toBeTruthy();
    expect(
      screen.getByText(
        "Original PM couldn’t reply. Your message is still here.",
      ),
    ).toBeTruthy();
    expect(screen.getByRole("textbox")).toHaveProperty("maxLength", 24000);
  });
  it("keeps a project's draft when leaving the tab", async () => {
    function Page() {
      const [draft, setDraft] = useState("");
      const [tab, setTab] = useState<"pm" | "activity">("pm");
      return (
        <>
          <button
            onClick={() => setTab((t) => (t === "pm" ? "activity" : "pm"))}
          >
            Switch
          </button>
          <ProjectPage
            project={project}
            state={state}
            route={{ page: "project", id: "p", tab }}
            refresh={async () => {}}
            pmDraft={draft}
            onPMDraft={setDraft}
          />
        </>
      );
    }
    render(<Page />);
    fireEvent.change(screen.getByRole("textbox"), {
      target: { value: "Kept draft" },
    });
    fireEvent.click(screen.getByText("Switch"));
    expect(screen.queryByRole("textbox")).toBeNull();
    fireEvent.click(screen.getByText("Switch"));
    expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe(
      "Kept draft",
    );
  });
  it("has text-only help and focuses the right large editor beside the assistant composer", async () => {
    render(
      <>
        <Composer
          id="chat-message"
          value="Assistant draft"
          onChange={() => {}}
          onSubmit={() => {}}
          name="Milo"
          onExpand={() => {}}
        />
        <Chat />
      </>,
    );
    const pm = screen.getByRole("region", { name: "PM chat" });
    expect(within(pm).queryByText(/drop text files/)).toBeNull();
    fireEvent.change(within(pm).getByRole("textbox"), {
      target: { value: "PM draft" },
    });
    fireEvent.click(
      within(pm).getByRole("button", { name: "Write in a larger space" }),
    );
    const dialog = screen.getByRole("dialog");
    const field = within(dialog).getByRole("textbox") as HTMLTextAreaElement;
    expect(field.value).toBe("PM draft");
    expect(document.activeElement).toBe(field);
    fireEvent.keyDown(field, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.activeElement?.id).toBe("pm-message-p");
    expect(
      (document.getElementById("chat-message") as HTMLTextAreaElement).value,
    ).toBe("Assistant draft");
  });
});

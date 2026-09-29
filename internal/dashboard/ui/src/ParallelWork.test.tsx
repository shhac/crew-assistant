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
import { ProjectPage } from "./ProjectPage";
import { requestHref, type ProjectTab } from "./router";
import {
  normalizeState,
  type Member,
  type MemberKind,
  type Playbook,
  type Project,
  type Role,
  type Task,
  type Turn,
} from "./api";

// Several requests under way at once: two implementer seats filled from one
// member, and a reviewer and QA who check one draft side by side.
const now = new Date("2026-09-29T10:00:00Z");
const ago = (ms: number) => new Date(now.valueOf() - ms).toISOString();
const face = (
  id: string,
  name: string,
  kind: MemberKind,
  c: string,
): Member => ({
  id,
  name,
  kinds: [kind],
  engine: "claude",
  avatar: { image: c.repeat(32) },
  learnings: [],
});
const claudius = face("m1", "Claudius", "implementer", "a");
const rune = face("m2", "Rune", "reviewer", "b");
const quinn = face("m3", "Quinn", "qa", "c");
const members = [claudius, rune, quinn];
const small = (m: Member) => `/api/avatars/${m.avatar!.image}/small`;
const roles: Role[] = [
  { name: "Claudius", kinds: ["implementer"], engine: "claude", member: "m1" },
  {
    name: "Claudius #2",
    kinds: ["implementer"],
    engine: "claude",
    member: "m1",
  },
  { name: "Rune", kinds: ["reviewer"], engine: "codex", member: "m2" },
  { name: "Quinn", kinds: ["qa"], engine: "codex", member: "m3" },
];
const team = (overrides: Partial<Playbook> = {}): Playbook => ({
  template: "code",
  medium: "git",
  roles,
  max_rounds: 3,
  deliver: "owner",
  repo: "/work/service",
  branch_prefix: "paul/",
  check: "make check",
  land: { via: "push", target: "main", approve: "before" },
  ...overrides,
});
const project = (playbook = team({ max_active: 3 })): Project => ({
  id: "p1",
  title: "Service",
  status: "active",
  brief: { version: 1, goal: "Make it faster", criteria: [] },
  playbook,
  directories: ["/work/service"],
});
const task = (n: number, overrides: Partial<Task>): Task => ({
  id: `t${n}`,
  ref: `CA-${n}`,
  project_id: "p1",
  objective: `Request ${n}`,
  criteria: [],
  status: "queued",
  stage: "todo",
  round: 1,
  revisions: [],
  verdicts: [],
  roles,
  playbook: team(),
  ...overrides,
});
const writing = (n: number, seat: string, overrides: Partial<Task> = {}) =>
  task(n, {
    status: "writing",
    stage: "implementing",
    claims: [{ step: "writing", seat }],
    ...overrides,
  });
const checked = (n: number) =>
  task(n, {
    status: "reviewing",
    stage: "reviewing",
    checking: "Rune",
    revisions: [{ n: 1, brief_version: 1, files: [] }],
    claims: [
      { step: "reviewing", seat: "Rune", shared: true },
      { step: "reviewing", seat: "Quinn", shared: true },
    ],
  });
const turn = (taskID: string, seat: string, role: Turn["role"]): Turn => ({
  project_id: "p1",
  task_id: taskID,
  role,
  seat,
  member: roles.find((r) => r.name === seat)?.member,
  started_at: ago(5 * 60_000),
  last_activity_at: ago(4_000),
  tool_calls: 3,
  edits: 0,
  output_tokens: 0,
});
/** Three requests under way at the cap of 3, and a fourth held by it. */
const busy = () => ({
  tasks: [
    writing(1, "Claudius"),
    writing(2, "Claudius #2"),
    checked(3),
    task(4, { waiting: { kind: "project_cap", active: 3, cap: 3 } }),
  ],
  turns: [
    turn("t1", "Claudius", "implementer"),
    turn("t2", "Claudius #2", "implementer"),
    turn("t3", "Rune", "reviewer"),
    turn("t3", "Quinn", "qa"),
  ],
});

let calls: { path: string; method: string; body: unknown }[];
const refresh = vi.fn(async () => {});
beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(now);
  calls = [];
  refresh.mockClear();
  sessionStorage.clear();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, options: RequestInit = {}) => {
      calls.push({
        path,
        method: options.method ?? "GET",
        body: options.body ? JSON.parse(options.body as string) : undefined,
      });
      return { ok: true, status: 200, json: async () => ({ steps: [] }) };
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

type Extra = { tasks?: Task[]; turns?: Turn[] };
const page = (
  p: Project,
  extra: Extra,
  at: { tab?: ProjectTab; request?: string; seat?: string } = {},
) => (
  <ProjectPage
    project={p}
    route={{
      page: "project",
      id: p.id,
      tab: at.tab ?? "board",
      request: at.request,
      seat: at.seat,
    }}
    state={normalizeState({
      assistant: { name: "Iris", personality: "" },
      projects: [p],
      members,
      ...extra,
    })}
    refresh={refresh}
  />
);
const show = (...args: Parameters<typeof page>) => render(page(...args));
const writes = () => calls.filter((c) => c.method !== "GET");
const card = (objective: string) =>
  screen.getByRole("link", { name: objective }).closest("article")!;
const step = (objective: string) =>
  card(objective).querySelector(".board-card-step")?.textContent;
const faces = (element: Element) =>
  [...element.querySelectorAll("img")].map((i) => i.getAttribute("src"));
const lane = (name: string) =>
  within(screen.getByRole("listitem", { name })).getAllByRole("link", {
    name: /^Request/,
  });
const seat = (name: string) =>
  document.querySelector<HTMLElement>(`button[data-seat="${name}"]`)!;

describe("several requests under way at once", () => {
  it("shows each on the board in its stage, with the seats at work on it", () => {
    show(project(), busy());
    expect(lane("Implementing").map((a) => a.textContent)).toEqual([
      "Request 1",
      "Request 2",
    ]);
    expect(lane("Reviewing").map((a) => a.textContent)).toEqual(["Request 3"]);
    // The second seat filled from Claudius is named, not the first.
    expect(step("Request 1")).toBe("Claudius working");
    expect(step("Request 2")).toBe("Claudius #2 working");
    expect(faces(card("Request 2"))).toEqual([small(claudius)]);
    // A reviewer and QA checking one draft both show.
    expect(step("Request 3")).toBe("Rune reviewing · Quinn running make check");
    expect(faces(card("Request 3"))).toEqual([small(rune), small(quinn)]);
    expect(
      [...card("Request 3").querySelectorAll(".task-activity")].map(
        (p) => p.textContent,
      ),
    ).toEqual([
      "Rune: Working · 5m · 3 calls · active 4s ago",
      "Quinn: Working · 5m · 3 calls · active 4s ago",
    ]);
    expect(step("Request 4")).toBe(
      "Waiting for this project's cap (3 of 3 active)",
    );
    expect(document.querySelector(".board-cap")?.textContent).toBe(
      "3 of 3 under way · Tasks at once",
    );
  });

  it("names the seat that took the step before its turn shows", () => {
    show(project(), { tasks: [writing(2, "Claudius #2"), checked(3)] });
    expect(step("Request 2")).toBe("With Claudius #2");
    // A reviewer and QA that have both taken a check are both named, on the
    // card and in the request's own view.
    expect(step("Request 3")).toBe("With Rune and Quinn");
    expect(faces(card("Request 3"))).toEqual([small(rune), small(quinn)]);
    cleanup();
    show(project(), { tasks: [checked(3)] }, { request: "t3" });
    const view = screen.getByRole("complementary", { name: "Request 3" });
    expect(view.querySelector(".request-status")?.textContent).toBe(
      "With Rune and Quinn",
    );
    expect(within(seat("Rune")).getByText("Working now")).toBeTruthy();
    expect(within(seat("Quinn")).getByText("Working now")).toBeTruthy();
  });

  it("marks every seat at work on a request in its view", () => {
    show(project(), busy(), { request: "t2" });
    const view = screen.getByRole("complementary", { name: "Request 2" });
    expect(view.querySelector(".request-status")?.textContent).toBe(
      "Claudius #2 working",
    );
    expect(within(seat("Claudius #2")).getByText("Working now")).toBeTruthy();
    expect(within(seat("Claudius")).queryByText("Working now")).toBeNull();
    expect(within(seat("Claudius")).queryByText("Up next")).toBeNull();
    cleanup();
    show(project(), busy(), { request: "t3" });
    expect(within(seat("Rune")).getByText("Working now")).toBeTruthy();
    expect(within(seat("Quinn")).getByText("Working now")).toBeTruthy();
    expect(within(seat("Claudius #2")).queryByText("Working now")).toBeNull();
  });

  it("says on the Team tab which request each seat is on, and where it is", () => {
    show(
      project(),
      { tasks: [writing(2, "Claudius #2"), checked(3)] },
      {
        tab: "team",
      },
    );
    const row = (name: string) =>
      within(screen.getByRole("list", { name: "Seats" }))
        .getAllByRole("listitem")
        .find((li) => li.querySelector(".seat-role")?.textContent === name)!;
    expect(row("Claudius").querySelector(".seat-who")?.textContent).toBe(
      "Implementer · Claude",
    );
    const on = within(row("Claudius #2")).getByRole("link", {
      name: "On CA-2 · Implementing",
    });
    expect(on.getAttribute("href")).toBe(requestHref("p1", "t2"));
    expect(
      within(row("Quinn")).getByRole("link", { name: "On CA-3 · Reviewing" }),
    ).toBeTruthy();
  });

  it("counts against the cap only the requests the cap counts", () => {
    show(project(team()), {
      tasks: [
        writing(1, "Claudius"),
        task(2, { status: "waiting", stage: "reviewing" }),
        task(3, { status: "awaiting", stage: "ready" }),
        task(4, {}),
      ],
    });
    // Unset, the cap is one per implementer seat.
    expect(document.querySelector(".board-cap")?.textContent).toBe(
      "1 of 2 under way · Tasks at once",
    );
  });
});

describe("the project's cap on requests under way", () => {
  const card = () => screen.getByRole("region", { name: "Tasks at once" });
  it("reads as one per implementer seat until set", () => {
    show(project(team()), {}, { tab: "config" });
    expect(card().querySelector("dd")?.textContent).toBe(
      "Up to 2 at once (one per implementer seat)",
    );
    cleanup();
    show(project(team({ max_active: 3 })), {}, { tab: "config" });
    expect(card().querySelector("dd")?.textContent).toBe("Up to 3 at once");
  });
  it("sets a number, or goes back to one per implementer seat", async () => {
    show(project(team()), {}, { tab: "config" });
    fireEvent.click(within(card()).getByRole("button", { name: "Edit" }));
    const field = within(card()).getByLabelText(/Requests under way at once/);
    expect((field as HTMLSelectElement).value).toBe("0");
    fireEvent.change(field, { target: { value: "4" } });
    fireEvent.click(within(card()).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/projects/p1/parallel",
        method: "PUT",
        body: { max_active: 4 },
      },
    ]);
    cleanup();
    calls = [];
    refresh.mockClear();
    show(project(team({ max_active: 4 })), {}, { tab: "config" });
    fireEvent.click(within(card()).getByRole("button", { name: "Edit" }));
    fireEvent.change(
      within(card()).getByLabelText(/Requests under way at once/),
      { target: { value: "0" } },
    );
    fireEvent.click(within(card()).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/projects/p1/parallel",
        method: "PUT",
        body: { max_active: 0 },
      },
    ]);
  });
});

describe("a refresh while several requests move", () => {
  it("keeps the open request, its member's panel, the draft and focus", async () => {
    const at = { request: "t2", seat: "Claudius #2" };
    const view = show(project(), busy(), at);
    const panel = await screen.findByRole("region", { name: "Claudius #2" });
    const box = within(panel).getByLabelText("Message") as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "Keep the old API" } });
    box.focus();
    // The next poll: request 1 has gone to review and request 3 has passed.
    const moved = busy();
    moved.tasks[0] = checked(1);
    moved.tasks[2] = task(3, { status: "landing", stage: "ready" });
    moved.turns = [
      turn("t2", "Claudius #2", "implementer"),
      turn("t1", "Rune", "reviewer"),
    ];
    view.rerender(page(project(), moved, at));
    expect(screen.getByRole("region", { name: "Claudius #2" })).toBe(panel);
    expect(box.isConnected).toBe(true);
    expect(box.value).toBe("Keep the old API");
    expect(document.activeElement).toBe(box);
    expect(step("Request 1")).toBe("Rune reviewing · Quinn running make check");
    expect(step("Request 2")).toBe("Claudius #2 working");
  });
});

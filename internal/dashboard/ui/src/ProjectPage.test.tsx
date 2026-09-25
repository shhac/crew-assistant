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
import type { ProjectTab } from "./router";
import {
  normalizeState,
  type Decision,
  type LandPolicy,
  type Member,
  type Playbook,
  type Project,
  type Role,
  type State,
  type Task,
} from "./api";

const writingTeam: Playbook = {
  template: "draft",
  medium: "documents",
  roles: [
    { name: "Writer", kinds: ["implementer"], engine: "claude" },
    { name: "Reviewer", kinds: ["reviewer"], engine: "codex" },
  ],
  max_rounds: 3,
  deliver: "owner",
};
const codeTeam = (
  land: LandPolicy = { via: "push", target: "main", approve: "before" },
): Playbook => ({
  template: "code",
  medium: "git",
  roles: [
    { name: "Implementer", kinds: ["implementer"], engine: "claude" },
    { name: "Reviewer", kinds: ["reviewer"], engine: "codex" },
    { name: "QA", kinds: ["qa"], engine: "codex" },
  ],
  max_rounds: 3,
  deliver: "owner",
  repo: "/work/service",
  branch_prefix: "paul/",
  check: "make check",
  land,
});
const project = (overrides: Partial<Project> = {}): Project => ({
  id: "p1",
  title: "Service",
  status: "active",
  brief: {
    version: 2,
    goal: "Make the service faster",
    criteria: ["p95 under 200ms"],
    updated_at: "2026-09-20T10:00:00Z",
  },
  playbook: codeTeam(),
  directories: ["/work/service", "/work/notes"],
  ...overrides,
});
const task = (overrides: Partial<Task> = {}): Task => ({
  id: "t1",
  project_id: "p1",
  objective: "Cache the lookups",
  criteria: [],
  status: "queued",
  stage: "todo",
  round: 1,
  revisions: [],
  verdicts: [],
  created_at: "2026-09-21T10:00:00Z",
  ...overrides,
});
const started = (overrides: Partial<Task> = {}) =>
  task({ roles: codeTeam().roles, playbook: codeTeam(), ...overrides });

let calls: { path: string; method: string; body: unknown }[];
let files: Record<number, unknown>;
const refresh = vi.fn(async () => {});
beforeEach(() => {
  calls = [];
  files = {};
  refresh.mockClear();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, options: RequestInit = {}) => {
      calls.push({
        path,
        method: options.method ?? "GET",
        body: options.body ? JSON.parse(options.body as string) : undefined,
      });
      const revision = /\/revisions\/(\d+)$/.exec(path);
      return {
        ok: true,
        status: 200,
        json: async () =>
          revision ? { files: files[Number(revision[1])] } : {},
      };
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function show(
  p: Project,
  extra: {
    tasks?: Task[];
    decisions?: Decision[];
    activity?: State["activity"];
    members?: Member[];
  } = {},
  at: { tab?: ProjectTab; request?: string } = {},
) {
  const state = normalizeState({
    assistant: { name: "Iris", personality: "" },
    projects: [p],
    ...extra,
  });
  return render(
    <ProjectPage
      project={p}
      route={{
        page: "project",
        id: p.id,
        tab: at.tab ?? "board",
        request: at.request,
      }}
      state={state}
      refresh={refresh}
    />,
  );
}
const writes = () => calls.filter((c) => c.method !== "GET");

describe("the board", () => {
  it("puts each request in its stage, with what needs the owner marked", () => {
    const delivery: Decision = {
      id: "d1",
      kind: "delivery",
      task_id: "t4",
      project_id: "p1",
      title: "Land it",
      context: "",
      recommendation: "Approve",
      choices: ["Approve", "Request changes"],
      status: "open",
    };
    show(project(), {
      tasks: [
        task({ id: "t1", objective: "First" }),
        task({ id: "t2", objective: "Second" }),
        started({
          id: "t3",
          objective: "Third",
          status: "writing",
          stage: "implementing",
          round: 2,
        }),
        started({
          id: "t4",
          objective: "Fourth",
          status: "waiting",
          stage: "ready",
          decision_id: "d1",
        }),
      ],
      decisions: [delivery],
    });
    const column = (name: string) => screen.getByRole("listitem", { name });
    expect(
      within(column("To do"))
        .getAllByRole("link")
        .map((a) => a.textContent),
    ).toEqual(["First", "Second"]);
    expect(
      within(column("Implementing")).getByText("Round 2 · Implementer working"),
    ).toBeTruthy();
    expect(
      within(column("Ready to land")).getByText("Waiting for your approval"),
    ).toBeTruthy();
    expect(within(column("QA")).queryAllByRole("link")).toHaveLength(0);
    expect(screen.getByText("1 needs you")).toBeTruthy();
  });
  it("shows a request being planned in its own column, with its planner", () => {
    const planner: Member = {
      id: "m1",
      name: "Ada",
      kinds: ["planner", "implementer"],
      engine: "claude",
      avatar: { image: "a".repeat(32) },
      learnings: [],
    };
    const roles = [
      {
        name: "Ada",
        kinds: ["implementer", "planner"],
        engine: "claude",
        member: "m1",
      },
      ...codeTeam().roles.slice(1),
    ];
    show(project({ playbook: { ...codeTeam(), roles } }), {
      members: [planner],
      tasks: [
        started({
          roles,
          status: "planning",
          stage: "planning",
          checking: "Ada",
        }),
      ],
    });
    const columns = screen
      .getAllByRole("listitem")
      .filter((c) => c.classList.contains("board-column"))
      .map((c) => c.getAttribute("aria-label"));
    expect(columns.slice(0, 3)).toEqual(["To do", "Planning", "Implementing"]);
    const planning = screen.getByRole("listitem", { name: "Planning" });
    expect(within(planning).getByText("Ada planning")).toBeTruthy();
    expect(planning.querySelector("img")?.getAttribute("src")).toBe(
      `/api/avatars/${"a".repeat(32)}/small`,
    );
  });
  it("says what a queued request waits for in place of its place in line", () => {
    show(project(), {
      tasks: [
        task({ id: "a", objective: "A", waits_for: ["Cache the lookups"] }),
        task({ id: "b", objective: "B" }),
        task({ id: "c", objective: "C", waits_for: ["X", "Y"] }),
      ],
    });
    const todo = screen.getByRole("listitem", { name: "To do" });
    expect(
      [...todo.querySelectorAll(".reorder > span")].map((s) => s.textContent),
    ).toEqual(["Waits for “Cache the lookups”", "#2", "Waits for “X”, “Y”"]);
    expect(screen.queryByRole("listitem", { name: "Planning" })).toBeNull();
    cleanup();
    show(project(), { tasks: [task({ waits_for: ["Cache the index"] })] });
    expect(screen.getByText("Waits for “Cache the index”")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Move/ })).toBeNull();
  });
  it("says quietly who set the to-do order, or that the PM is looking at it", () => {
    const two = [
      task({ id: "a", objective: "A" }),
      task({ id: "b", objective: "B" }),
    ];
    const todo = () => screen.getByRole("listitem", { name: "To do" });
    const line = () => todo().querySelector(".todo-order")?.textContent;
    show(project({ ordered_by: "owner" }), { tasks: two });
    expect(line()).toBe("Ordered by you");
    cleanup();
    const pia: Role = {
      name: "Pia",
      kinds: ["pm"],
      engine: "claude",
      member: "m4",
    };
    const kept = { ...codeTeam(), roles: [...codeTeam().roles, pia] };
    show(project({ playbook: kept, ordered_by: "pm" }), { tasks: two });
    expect(line()).toBe("Ordered by Pia");
    cleanup();
    show(project({ playbook: kept, ordered_by: "pm", pm_due: true }), {
      tasks: two,
    });
    expect(line()).toBe("Pia is looking at the order");
    cleanup();
    show(project(), { tasks: [task({})] });
    expect(line()).toBeUndefined();
  });
  it("reorders the to-do list in the order work starts in", async () => {
    show(project(), {
      tasks: [
        task({ id: "a", objective: "A" }),
        task({ id: "b", objective: "B" }),
      ],
    });
    expect(screen.getByRole("button", { name: "Move “A” up" })).toHaveProperty(
      "disabled",
      true,
    );
    fireEvent.click(screen.getByRole("button", { name: "Move “B” up" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/projects/p1/tasks/order",
        method: "PUT",
        body: { task_ids: ["b", "a"] },
      },
    ]);
  });
  it("moves a request down the to-do list", async () => {
    show(project(), {
      tasks: [
        task({ id: "a", objective: "A" }),
        task({ id: "b", objective: "B" }),
      ],
    });
    expect(
      screen.getByRole("button", { name: "Move “B” down" }),
    ).toHaveProperty("disabled", true);
    fireEvent.click(screen.getByRole("button", { name: "Move “A” down" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/projects/p1/tasks/order",
        method: "PUT",
        body: { task_ids: ["b", "a"] },
      },
    ]);
  });
  it("drops a dragged request where it lands in the to-do list", async () => {
    show(project(), {
      tasks: [
        task({ id: "a", objective: "A" }),
        task({ id: "b", objective: "B" }),
        task({ id: "c", objective: "C" }),
      ],
    });
    const card = (name: string) => {
      const item = screen.getByRole("link", { name }).closest("li");
      if (!item) throw new Error(`No card for ${name}`);
      return item;
    };
    fireEvent.dragStart(card("A"), { dataTransfer: { effectAllowed: "" } });
    fireEvent.dragOver(card("C"));
    fireEvent.drop(card("C"));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/projects/p1/tasks/order",
        method: "PUT",
        body: { task_ids: ["b", "c", "a"] },
      },
    ]);
  });
  it("fetches the current to-do list when a reorder is refused", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: false,
        status: 409,
        json: async () => ({ error: "The to-do list changed; try again" }),
      })),
    );
    show(project(), {
      tasks: [
        task({ id: "a", objective: "A" }),
        task({ id: "b", objective: "B" }),
      ],
    });
    expect(refresh).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Move “B” up" }));
    expect((await screen.findByRole("alert")).textContent).toBe(
      "The to-do list changed; try again",
    );
    expect(refresh).toHaveBeenCalled();
  });
  it("asks the team for something, with how it will be judged", async () => {
    show(project());
    fireEvent.change(screen.getByLabelText("What do you want?"), {
      target: { value: "Cache the lookups" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "+ Say how you'll judge it" }),
    );
    fireEvent.change(screen.getByLabelText(/How you'll judge it/), {
      target: { value: "No stale reads\n\nUnder 1ms" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Ask" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/projects/p1/tasks",
        method: "POST",
        body: {
          objective: "Cache the lookups",
          criteria: ["No stale reads", "Under 1ms"],
        },
      },
    ]);
    expect(screen.getByLabelText("What do you want?")).toHaveProperty(
      "value",
      "",
    );
  });
  it("says what's missing before anything can be asked", () => {
    show(project({ brief: { version: 0, goal: "", criteria: [] } }));
    expect(screen.getByText(/Needs a brief first/)).toBeTruthy();
    expect(
      screen
        .getByRole("link", { name: "Write the brief" })
        .getAttribute("href"),
    ).toBe("#/projects/p1/brief");
    cleanup();
    show(project({ playbook: undefined }));
    expect(
      screen.getByRole("link", { name: "Choose a team" }).getAttribute("href"),
    ).toBe("#/projects/p1/team");
    expect(screen.queryByRole("tab", { name: "Landing" })).toBeNull();
  });
});

describe("a request", () => {
  const delivery: Decision = {
    id: "d1",
    kind: "delivery",
    task_id: "t1",
    project_id: "p1",
    title: "Land “Cache the lookups” on main",
    context: "Added a cache.",
    recommendation: "Approve",
    choices: ["Approve", "Request changes"],
    status: "open",
  };
  const waiting = () =>
    started({
      status: "waiting",
      stage: "ready",
      decision_id: "d1",
      revisions: [
        {
          n: 1,
          brief_version: 2,
          files: ["cache.go", "cache_test.go"],
          ref: "abc1234def",
          summary: "Added a cache.",
        },
      ],
      verdicts: [
        {
          revision: 1,
          role: "Reviewer",
          brief_version: 2,
          outcome: "pass",
          summary: "Looks right.",
        },
        {
          revision: 1,
          role: "QA",
          brief_version: 2,
          outcome: "pass",
          summary: "make check passed.",
        },
      ],
    });
  it("shows its approval in full and lands with one click", async () => {
    show(
      project(),
      { tasks: [waiting()], decisions: [delivery] },
      { request: "t1" },
    );
    const panel = screen.getByRole("complementary", {
      name: "Cache the lookups",
    });
    expect(within(panel).getByText("Undoable with effort")).toBeTruthy();
    expect(panel.querySelector(".decision-context")?.textContent).toBe(
      "Added a cache.",
    );
    // The latest change is folded while its approval is showing.
    expect(panel.querySelector("details.draft")?.hasAttribute("open")).toBe(
      false,
    );
    expect(
      within(panel).queryByRole("button", { name: "Close without deciding" }),
    ).toBeNull();
    fireEvent.click(
      within(panel).getByRole("button", { name: "Land on main" }),
    );
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/decisions/d1/resolve",
        method: "POST",
        body: { choice: "Approve" },
      },
    ]);
  });
  it("asks before pushing an update that changes what runs, in its own words", async () => {
    const update: Decision = {
      ...delivery,
      kind: "update",
      title: "Check the update to “Cache the lookups” before it's pushed",
    };
    show(
      project({
        playbook: codeTeam({
          via: "pull-request",
          target: "main",
          github: "o/r",
        }),
      }),
      { tasks: [waiting()], decisions: [update] },
      { request: "t1" },
    );
    expect(
      screen.queryByRole("button", { name: "Open pull request" }),
    ).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Push the update" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/decisions/d1/resolve",
        method: "POST",
        body: { choice: "Approve" },
      },
    ]);
  });
  it("asks what should change and sends it as the answer", async () => {
    show(
      project(),
      { tasks: [waiting()], decisions: [delivery] },
      { request: "t1" },
    );
    fireEvent.click(screen.getByRole("button", { name: "Request changes" }));
    fireEvent.change(screen.getByLabelText("What should change?"), {
      target: { value: "Expire entries after a minute" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Send changes" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/decisions/d1/resolve",
        method: "POST",
        body: { answer: "Expire entries after a minute" },
      },
    ]);
  });
  it("messages one member of the team and shows their reply", async () => {
    const replied = started({
      status: "reviewing",
      stage: "qa",
      revisions: [{ n: 1, brief_version: 2, files: [] }],
      messages: [
        {
          id: "m1",
          to: "Reviewer",
          kind: "reviewer",
          direction: 0,
          from: "owner",
          text: "Is the cache safe?",
          status: "answered",
          outcome: "pass",
          revision: 1,
          reply: "Yes; it is keyed by tenant.",
        },
      ],
    });
    show(project(), { tasks: [replied] }, { request: "t1" });
    expect(screen.getByText("Is the cache safe?")).toBeTruthy();
    expect(screen.getByText("Yes; it is keyed by tenant.")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "QA" } });
    expect(
      screen.getByText("QA checks the latest draft now, with your note."),
    ).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Message"), {
      target: { value: "Run it with the race detector" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Send to QA" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/projects/p1/tasks/t1/messages",
        method: "POST",
        body: { to: "QA", text: "Run it with the race detector" },
      },
    ]);
  });
  it("prompts by the kind of role, never lowercasing a member's name", () => {
    const roles = [
      { name: "Ada", kinds: ["implementer"], engine: "claude", member: "m1" },
      { name: "Rune", kinds: ["reviewer"], engine: "codex", member: "m2" },
      { name: "QA", kinds: ["qa"], engine: "codex" },
    ];
    show(
      project(),
      {
        tasks: [started({ status: "writing", stage: "implementing", roles })],
      },
      { request: "t1" },
    );
    const box = screen.getByLabelText("Message");
    expect(box.getAttribute("placeholder")).toBe(
      "Tell the implementer what to change",
    );
    fireEvent.change(screen.getByLabelText("To"), {
      target: { value: "Rune" },
    });
    expect(box.getAttribute("placeholder")).toBe(
      "Ask the reviewer to check something",
    );
    expect(screen.getByRole("button", { name: "Send to Rune" })).toBeTruthy();
  });
  it("leaves out a seat that only plans, and names a seat that also plans by its work", () => {
    const planner = { name: "Planner", kinds: ["planner"], engine: "claude" };
    show(
      project(),
      {
        tasks: [
          started({
            status: "planning",
            stage: "planning",
            checking: "Planner",
            roles: [planner, ...codeTeam().roles],
          }),
        ],
      },
      { request: "t1" },
    );
    const options = () =>
      within(screen.getByLabelText("To"))
        .getAllByRole("option")
        .map((o) => o.textContent);
    expect(options()).toEqual(["Implementer", "Reviewer", "QA"]);
    expect(screen.getByText("Goes into the first round.")).toBeTruthy();
    cleanup();
    const ada = {
      name: "Ada",
      kinds: ["implementer", "planner"],
      engine: "claude",
    };
    show(
      project(),
      {
        tasks: [
          started({
            status: "writing",
            stage: "implementing",
            roles: [ada, ...codeTeam().roles.slice(1)],
          }),
        ],
      },
      { request: "t1" },
    );
    expect(options()).toEqual(["Ada (implementer)", "Reviewer", "QA"]);
    expect(screen.getByLabelText("Message").getAttribute("placeholder")).toBe(
      "Tell the implementer what to change",
    );
    expect(screen.getByRole("button", { name: "Send to Ada" })).toBeTruthy();
  });
  it("leaves out a seat that only keeps the to-do list, and names one that also works by its work", () => {
    const pia = { name: "Pia", kinds: ["pm"], engine: "claude" };
    const rune = { name: "Rune", kinds: ["reviewer", "pm"], engine: "codex" };
    const [implementer, , qa] = codeTeam().roles;
    show(
      project(),
      {
        tasks: [
          started({
            status: "writing",
            stage: "implementing",
            roles: [implementer, rune, qa, pia],
          }),
        ],
      },
      { request: "t1" },
    );
    expect(
      within(screen.getByLabelText("To"))
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["Implementer", "Rune (reviewer)", "QA"]);
  });
  it("shows the plan with only the parts it has, and who planned it", () => {
    show(
      project(),
      {
        tasks: [
          started({
            status: "writing",
            stage: "implementing",
            plan: {
              summary: "Wrap the lookup in a cache.",
              exists: ["A lookup in store.go"],
              changes: ["Add a cache in front of it"],
              role: "Planner",
              at: new Date(Date.now() - 5 * 60000).toISOString(),
            },
          }),
        ],
      },
      { request: "t1" },
    );
    const plan = screen.getByRole("region", { name: "Plan" });
    expect(within(plan).getByText("Wrap the lookup in a cache.")).toBeTruthy();
    expect(within(plan).getByText("What exists")).toBeTruthy();
    expect(within(plan).getByText("Add a cache in front of it")).toBeTruthy();
    expect(within(plan).queryByText("Out of scope")).toBeNull();
    expect(within(plan).queryByRole("button")).toBeNull();
    expect(
      within(plan).getByText("Planned by Planner · 5 min ago"),
    ).toBeTruthy();
  });
  it("folds a long plan to its summary until asked", () => {
    show(
      project(),
      {
        tasks: [
          started({
            status: "writing",
            stage: "implementing",
            plan: {
              summary: "Wrap the lookup in a cache.",
              exists: ["A lookup", "A store"],
              changes: ["A cache"],
              out_of_scope: ["Eviction"],
              role: "Ada",
              at: "2026-09-21T10:00:00Z",
            },
          }),
        ],
      },
      { request: "t1" },
    );
    const plan = screen.getByRole("region", { name: "Plan" });
    expect(within(plan).getByText("Wrap the lookup in a cache.")).toBeTruthy();
    expect(within(plan).queryByText("Eviction")).toBeNull();
    fireEvent.click(
      within(plan).getByRole("button", { name: "Show the plan" }),
    );
    expect(within(plan).getByText("Out of scope")).toBeTruthy();
    expect(within(plan).getByText("Eviction")).toBeTruthy();
    expect(within(plan).queryByRole("button")).toBeNull();
  });
  it("shows no plan for a request that has none", () => {
    show(
      project(),
      { tasks: [started({ status: "writing" })] },
      { request: "t1" },
    );
    expect(screen.queryByRole("region", { name: "Plan" })).toBeNull();
  });
  it("keeps an answer to a decision that repeats a message to the implementer", () => {
    const told = started({
      status: "writing",
      stage: "implementing",
      direction: ["Keep it short", "Keep it short"],
      messages: [
        {
          id: "m1",
          to: "Implementer",
          kind: "implementer",
          from: "owner",
          text: "Keep it short",
          status: "answered",
          direction: 0,
        },
      ],
    });
    show(project(), { tasks: [told] }, { request: "t1" });
    const panel = screen.getByRole("complementary", {
      name: "Cache the lookups",
    });
    expect(within(panel).getByText("Your answers along the way")).toBeTruthy();
    expect(
      [...panel.querySelectorAll("ul.said li")].map((li) => li.textContent),
    ).toEqual(["Keep it short"]);
  });
  it("stops a request that isn't finished, and offers nothing for one that is", async () => {
    show(
      project(),
      { tasks: [started({ status: "writing", stage: "implementing" })] },
      { request: "t1" },
    );
    fireEvent.click(screen.getByRole("button", { name: "Stop request" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      { path: "/api/projects/p1/tasks/t1/stop", method: "POST", body: {} },
    ]);
    cleanup();
    show(
      project(),
      {
        tasks: [
          started({ status: "landed", stage: "done", delivered_to: "main" }),
        ],
      },
      { request: "t1" },
    );
    expect(screen.queryByRole("button", { name: "Stop request" })).toBeNull();
    expect(screen.getByText(/This request is finished/)).toBeTruthy();
  });
  it("offers to land a change delivered before the project landed on main", async () => {
    show(
      project(),
      {
        tasks: [
          started({
            status: "delivered",
            stage: "done",
            delivered_to: "paul/cache",
          }),
        ],
      },
      { request: "t1" },
    );
    fireEvent.click(screen.getByRole("button", { name: "Land on main" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      { path: "/api/projects/p1/tasks/t1/land", method: "POST", body: {} },
    ]);
  });
  it("reads the latest draft of written work", async () => {
    files[2] = [{ path: "note.md", content: "# Hello", size: 7 }];
    const writing = project({ playbook: writingTeam });
    show(
      writing,
      {
        tasks: [
          task({
            status: "reviewing",
            stage: "reviewing",
            roles: writingTeam.roles,
            playbook: writingTeam,
            revisions: [
              { n: 1, brief_version: 2, files: ["note.md"] },
              { n: 2, brief_version: 2, files: ["note.md"] },
            ],
          }),
        ],
      },
      { request: "t1" },
    );
    expect(await screen.findByRole("heading", { name: "Hello" })).toBeTruthy();
    expect(
      calls.some((c) => c.path === "/api/projects/p1/tasks/t1/revisions/2"),
    ).toBe(true);
    expect(calls.some((c) => c.path.endsWith("/revisions/1"))).toBe(false);
  });
  it("stays open and leaves focus in a text field across a refresh", () => {
    window.history.replaceState(null, "", "/#/projects/p1/requests/t1");
    const view = show(project(), { tasks: [started()] }, { request: "t1" });
    const panel = screen.getByRole("complementary", {
      name: "Cache the lookups",
    });
    const field = document.body.appendChild(document.createElement("textarea"));
    try {
      field.focus();
      view.rerender(
        <ProjectPage
          project={project()}
          route={{ page: "project", id: "p1", tab: "board", request: "t1" }}
          state={normalizeState({
            assistant: { name: "Iris", personality: "" },
            projects: [project()],
            tasks: [started({ detail: "Writing the cache" })],
          })}
          refresh={refresh}
        />,
      );
      expect(screen.getByText("Writing the cache")).toBeTruthy();
      expect(document.activeElement).toBe(field);
      fireEvent.keyDown(field, { key: "Escape" });
      expect(window.location.hash).toBe("#/projects/p1/requests/t1");
      expect(panel.isConnected).toBe(true);
    } finally {
      field.remove();
    }
  });
  it("closes on Escape away from text fields", () => {
    window.history.replaceState(null, "", "/#/projects/p1/requests/t1");
    show(project(), { tasks: [started()] }, { request: "t1" });
    const field = document.body.appendChild(document.createElement("div"));
    field.setAttribute("contenteditable", "true");
    try {
      fireEvent.keyDown(field, { key: "Escape" });
      expect(window.location.hash).toBe("#/projects/p1/requests/t1");
      fireEvent.keyDown(screen.getByRole("button", { name: "Close" }), {
        key: "Escape",
      });
      expect(window.location.hash).toBe("#/projects/p1");
    } finally {
      field.remove();
    }
  });
});

describe("the project's tabs", () => {
  it("saves a brief edit as a new version", async () => {
    show(project(), {}, { tab: "brief" });
    expect(screen.getByText("p95 under 200ms")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    expect(screen.getByText(/Saving makes version 3/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Goal"), {
      target: { value: "Make it fast" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save brief" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/projects/p1/brief",
        method: "PUT",
        body: {
          goal: "Make it fast",
          audience: "",
          constraints: "",
          criteria: ["p95 under 200ms"],
        },
      },
    ]);
  });
  it("sets up a code team on one of the project's folders", async () => {
    show(project({ playbook: undefined }), {}, { tab: "team" });
    fireEvent.click(screen.getByRole("button", { name: "Choose a team" }));
    const team = screen.getByRole("form", { name: "Team" });
    expect(within(team).queryByLabelText("Repository")).toBeNull();
    fireEvent.change(within(team).getByLabelText("Kind of work"), {
      target: { value: "code" },
    });
    fireEvent.change(within(team).getByLabelText("Repository"), {
      target: { value: "/work/service" },
    });
    fireEvent.change(within(team).getByLabelText("QA runs"), {
      target: { value: "make check" },
    });
    fireEvent.change(within(team).getByLabelText("Branch prefix"), {
      target: { value: "paul/" },
    });
    fireEvent.change(
      within(team).getByLabelText(/^Ignored folders to copy in/),
      { target: { value: "ui/node_modules, vendor" } },
    );
    expect(within(team).getByLabelText("Sign commits")).toHaveProperty(
      "value",
      "",
    );
    fireEvent.change(within(team).getByLabelText("Sign commits"), {
      target: { value: "never" },
    });
    fireEvent.click(within(team).getByRole("button", { name: "Save team" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/projects/p1/team",
        method: "PUT",
        body: {
          template: "code",
          writer_engine: "claude",
          reviewer_engine: "codex",
          implementer_member: "",
          reviewer_member: "",
          qa_member: "",
          planner_member: "",
          pm_member: "",
          max_rounds: "3",
          deliver_to: "",
          repo: "/work/service",
          branch_prefix: "paul/",
          check: "make check",
          prepare: ["ui/node_modules", "vendor"],
          sign: "never",
        },
      },
    ]);
  });
  it("describes a code team by who is on it and how its commits are signed", () => {
    show(project(), {}, { tab: "team" });
    const team = screen.getByRole("region", { name: "Team" });
    expect(within(team).getByText("make check")).toBeTruthy();
    expect(within(team).getByText("/work/service")).toBeTruthy();
    expect(
      within(team).getByText("Signed as your git config says"),
    ).toBeTruthy();
    expect(screen.getByRole("region", { name: "Folders" })).toBeTruthy();
  });
  const member = (
    id: string,
    name: string,
    ...kinds: Member["kinds"]
  ): Member => ({
    id,
    name,
    kinds,
    engine: "claude",
    avatar_svg: `<svg xmlns="http://www.w3.org/2000/svg"><title>${name}</title></svg>`,
    learnings: [],
  });
  const crew = [
    member("m1", "Ada Lovelace", "implementer"),
    member("m2", "Rune", "reviewer"),
    member("m3", "Quinn", "qa"),
  ];
  const staffed = () =>
    project({
      playbook: {
        ...codeTeam(),
        roles: [
          {
            name: "Ada",
            kinds: ["implementer"],
            engine: "claude",
            member: "m1",
          },
          { name: "Reviewer", kinds: ["reviewer"], engine: "codex" },
          { name: "QA", kinds: ["qa"], engine: "codex" },
        ],
      },
    });
  it("shows a member by the name its role was given, with its face and page", () => {
    show(staffed(), { members: crew }, { tab: "team" });
    const team = screen.getByRole("region", { name: "Team" });
    const ada = within(team).getByRole("link", { name: "Ada" });
    expect(ada.getAttribute("href")).toBe("#/team/m1");
    expect(ada.querySelector("img")?.getAttribute("width")).toBe("20");
    expect(within(team).queryByText("Ada Lovelace")).toBeNull();
    expect(within(team).queryByRole("link", { name: "Reviewer" })).toBeNull();
  });
  it("keeps a team's members when it is saved again, and asks the engine only of the template's roles", async () => {
    show(staffed(), { members: crew }, { tab: "team" });
    fireEvent.click(
      within(screen.getByRole("region", { name: "Team" })).getByRole("button", {
        name: "Edit",
      }),
    );
    const team = screen.getByRole("form", { name: "Team" });
    const implementer = within(team).getByRole("group", {
      name: "Implementer",
    });
    expect(within(implementer).getByLabelText("Who")).toHaveProperty(
      "value",
      "m1",
    );
    expect(within(implementer).queryByLabelText("Engine")).toBeNull();
    const reviewer = within(team).getByRole("group", { name: "Reviewer" });
    expect(
      within(within(reviewer).getByLabelText("Who"))
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["Template default", "Rune"]);
    expect(within(reviewer).getByLabelText("Engine")).toBeTruthy();
    fireEvent.change(within(reviewer).getByLabelText("Who"), {
      target: { value: "m2" },
    });
    expect(within(reviewer).queryByLabelText("Engine")).toBeNull();
    const qa = within(team).getByRole("group", { name: "QA" });
    fireEvent.change(within(qa).getByLabelText("Who"), {
      target: { value: "m3" },
    });
    fireEvent.click(within(team).getByRole("button", { name: "Save team" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()[0].body).toMatchObject({
      template: "code",
      implementer_member: "m1",
      reviewer_member: "m2",
      qa_member: "m3",
    });
  });
  describe("a code team's planner", () => {
    const planners = [
      member("m1", "Ada Lovelace", "implementer", "planner"),
      member("m2", "Rune", "reviewer"),
      member("m4", "Pia", "planner"),
    ];
    const seats = (...roles: Role[]) =>
      project({ playbook: { ...codeTeam(), roles } });
    const ada: Role = {
      name: "Ada",
      kinds: ["implementer", "planner"],
      engine: "claude",
      member: "m1",
    };
    const planner: Role = {
      name: "Planner",
      kinds: ["planner"],
      engine: "claude",
    };
    const [, reviewer, qa] = codeTeam().roles;
    const edit = (p: Project) => {
      show(p, { members: planners }, { tab: "team" });
      fireEvent.click(
        within(screen.getByRole("region", { name: "Team" })).getByRole(
          "button",
          { name: "Edit" },
        ),
      );
      const row = screen.getByRole("group", { name: "Planner" });
      return within(row).getByLabelText("Who");
    };
    const saved = async () => {
      fireEvent.click(screen.getByRole("button", { name: "Save team" }));
      await waitFor(() => expect(refresh).toHaveBeenCalled());
      return writes()[0].body;
    };
    it("lists each seat with the kinds it holds", () => {
      show(
        seats(planner, ada, reviewer, qa),
        { members: planners },
        { tab: "team" },
      );
      const team = screen.getByRole("region", { name: "Team" });
      const rows = [...team.querySelectorAll(".fact-row")]
        .slice(0, 4)
        .map((r) => r.textContent);
      expect(rows).toEqual([
        "PlannerClaude",
        "AdaPlanner and implementer · Claude",
        "ReviewerCodex",
        "QACodex",
      ]);
    });
    it("keeps a member who plans from their own seat when saved again", async () => {
      const who = edit(seats(ada, reviewer, qa));
      expect(who).toHaveProperty("value", "m1");
      expect(
        within(who)
          .getAllByRole("option")
          .map((o) => o.textContent),
      ).toEqual(["Template default", "No planning", "Ada Lovelace", "Pia"]);
      expect(await saved()).toMatchObject({
        implementer_member: "m1",
        planner_member: "m1",
      });
    });
    it("keeps a team without planning that way, and can bring the template's back", async () => {
      const who = edit(seats(codeTeam().roles[0], reviewer, qa));
      expect(who).toHaveProperty("value", "none");
      expect(await saved()).toMatchObject({ planner_member: "none" });
      cleanup();
      calls = [];
      refresh.mockClear();
      fireEvent.change(edit(seats(codeTeam().roles[0], reviewer, qa)), {
        target: { value: "" },
      });
      expect(await saved()).toMatchObject({ planner_member: "" });
    });
    it("fills the template's planner with a member", async () => {
      const who = edit(seats(planner, codeTeam().roles[0], reviewer, qa));
      expect(who).toHaveProperty("value", "");
      fireEvent.change(who, { target: { value: "m4" } });
      expect(await saved()).toMatchObject({ planner_member: "m4" });
    });
  });
  describe("a team's PM", () => {
    const crewWithPM = [
      member("m1", "Ada Lovelace", "implementer", "pm"),
      member("m2", "Rune", "reviewer"),
      member("m4", "Pia", "pm"),
    ];
    const pia: Role = {
      name: "Pia",
      kinds: ["pm"],
      engine: "claude",
      member: "m4",
    };
    const edit = (p: Project, members = crewWithPM) => {
      show(p, { members }, { tab: "team" });
      fireEvent.click(
        within(screen.getByRole("region", { name: "Team" })).getByRole(
          "button",
          { name: "Edit" },
        ),
      );
      return screen.queryByRole("group", { name: "PM" });
    };
    const chooser = (p: Project) => {
      const slot = edit(p);
      if (!slot) throw new Error("No PM to choose");
      return within(slot).getByLabelText("Who");
    };
    const saved = async () => {
      fireEvent.click(screen.getByRole("button", { name: "Save team" }));
      await waitFor(() => expect(refresh).toHaveBeenCalled());
      return writes()[0].body;
    };
    it("lists the PM's seat with the kinds it holds", () => {
      const ada: Role = {
        name: "Ada",
        kinds: ["implementer", "pm"],
        engine: "claude",
        member: "m1",
      };
      const [, reviewer, qa] = codeTeam().roles;
      show(
        project({
          playbook: { ...codeTeam(), roles: [ada, reviewer, qa, pia] },
        }),
        { members: crewWithPM },
        { tab: "team" },
      );
      const rows = [
        ...screen
          .getByRole("region", { name: "Team" })
          .querySelectorAll(".fact-row"),
      ]
        .slice(0, 4)
        .map((r) => r.textContent);
      expect(rows).toEqual([
        "AdaImplementer and PM · Claude",
        "ReviewerCodex",
        "QACodex",
        "PiaPM · Claude",
      ]);
    });
    it("chooses a PM from the members who hold it, or none", async () => {
      const who = chooser(project());
      expect(who).toHaveProperty("value", "");
      expect(
        within(who)
          .getAllByRole("option")
          .map((o) => o.textContent),
      ).toEqual(["No PM", "Ada Lovelace", "Pia"]);
      expect(screen.getAllByLabelText("Engine")).toHaveLength(2);
      fireEvent.change(who, { target: { value: "m4" } });
      expect(await saved()).toMatchObject({ pm_member: "m4" });
    });
    it("keeps the PM when the team is saved again, for writing too", async () => {
      const who = chooser(
        project({
          playbook: { ...writingTeam, roles: [...writingTeam.roles, pia] },
        }),
      );
      expect(who).toHaveProperty("value", "m4");
      expect(await saved()).toMatchObject({
        template: "draft",
        pm_member: "m4",
      });
      cleanup();
      calls = [];
      refresh.mockClear();
      fireEvent.change(
        chooser(
          project({
            playbook: { ...codeTeam(), roles: [...codeTeam().roles, pia] },
          }),
        ),
        { target: { value: "" } },
      );
      expect(await saved()).toMatchObject({ pm_member: "" });
    });
    it("offers no PM while no member holds it", async () => {
      expect(edit(project(), crew)).toBeNull();
      expect(await saved()).toMatchObject({ pm_member: "" });
    });
  });
  it("offers no QA for a writing team and no choice where there are no members", async () => {
    show(
      project({ playbook: writingTeam }),
      { members: crew },
      { tab: "team" },
    );
    fireEvent.click(
      within(screen.getByRole("region", { name: "Team" })).getByRole("button", {
        name: "Edit",
      }),
    );
    const team = screen.getByRole("form", { name: "Team" });
    expect(within(team).getByRole("group", { name: "Writer" })).toBeTruthy();
    expect(within(team).queryByRole("group", { name: "QA" })).toBeNull();
    fireEvent.click(within(team).getByRole("button", { name: "Save team" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()[0].body).toMatchObject({
      implementer_member: "",
      reviewer_member: "",
      qa_member: "",
      planner_member: "",
    });
    expect(within(team).queryByRole("group", { name: "Planner" })).toBeNull();
    cleanup();
    show(project(), {}, { tab: "team" });
    fireEvent.click(
      within(screen.getByRole("region", { name: "Team" })).getByRole("button", {
        name: "Edit",
      }),
    );
    // Only planning can be left out, so only it offers a choice.
    expect(screen.getAllByLabelText("Who")).toHaveLength(1);
    expect(screen.queryByRole("group", { name: "QA" })).toBeNull();
    expect(screen.getAllByLabelText("Engine")).toHaveLength(2);
  });
  it("sets where a code team's changes land, on a tab of its own", async () => {
    show(project({ playbook: codeTeam({}) }), {}, { tab: "landing" });
    expect(screen.getByText("A new local branch")).toBeTruthy();
    expect(screen.getByText("Undoable")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    fireEvent.change(screen.getByLabelText("Lands as"), {
      target: { value: "push" },
    });
    fireEvent.change(screen.getByLabelText(/^What landing means here/), {
      target: { value: "fully ff-merged to main" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/projects/p1/landing",
        method: "PUT",
        body: {
          means: "fully ff-merged to main",
          via: "push",
          target: "main",
          method: "fast-forward",
          github: "",
          approve: "before",
        },
      },
    ]);
  });
  it("shows what happened, with each step of work only on request", () => {
    show(
      project(),
      {
        activity: [
          {
            id: "a1",
            project_id: "p1",
            kind: "task.landed",
            summary: "Cache the lookups landed on main",
            created_at: "2026-09-21T11:00:00Z",
          },
          {
            id: "a2",
            project_id: "p1",
            kind: "task.reviewing",
            summary: "Reviewer checked draft 1",
            created_at: "2026-09-21T10:30:00Z",
          },
          {
            id: "a3",
            project_id: "other",
            kind: "task.landed",
            summary: "Something elsewhere",
            created_at: "2026-09-21T10:00:00Z",
          },
        ],
      },
      { tab: "activity" },
    );
    expect(screen.getByText("Cache the lookups landed on main")).toBeTruthy();
    expect(screen.queryByText("Reviewer checked draft 1")).toBeNull();
    expect(screen.queryByText("Something elsewhere")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Show all steps (1)" }));
    expect(screen.getByText("Reviewer checked draft 1")).toBeTruthy();
  });
});

describe("faces of the team at work", () => {
  const face = (id: string, name: string, image: string): Member => ({
    id,
    name,
    kinds: ["implementer"],
    engine: "claude",
    avatar: { image },
    learnings: [],
  });
  const ada = face("m1", "Ada", "a".repeat(32));
  const rune = face("m2", "Rune", "b".repeat(32));
  const roles = [
    { name: "Ada", kinds: ["implementer"], engine: "claude", member: "m1" },
    { name: "Rune", kinds: ["reviewer"], engine: "codex", member: "m2" },
    { name: "QA", kinds: ["qa"], engine: "codex" },
  ];
  const staffed = (overrides: Partial<Task> = {}) =>
    task({ roles, playbook: { ...codeTeam(), roles }, ...overrides });
  const faces = (element: HTMLElement) =>
    [...element.querySelectorAll("img")].map((i) => i.getAttribute("src"));

  it("shows who is at work on each card: the implementer writing, the checker checking", () => {
    show(project({ playbook: { ...codeTeam(), roles } }), {
      members: [ada, rune],
      tasks: [
        staffed({
          id: "t1",
          objective: "Writes",
          status: "writing",
          stage: "implementing",
        }),
        staffed({
          id: "t2",
          objective: "Reviewed",
          status: "reviewing",
          stage: "reviewing",
          checking: "Rune",
        }),
        staffed({
          id: "t3",
          objective: "Checked by QA",
          status: "reviewing",
          stage: "qa",
          checking: "QA",
        }),
        staffed({ id: "t4", objective: "Waits" }),
      ],
    });
    const card = (name: string) =>
      screen.getByRole("link", { name }).closest("article")!;
    expect(faces(card("Writes"))).toEqual([
      `/api/avatars/${"a".repeat(32)}/small`,
    ]);
    expect(card("Writes").querySelector("img")?.getAttribute("width")).toBe(
      "20",
    );
    expect(faces(card("Reviewed"))).toEqual([
      `/api/avatars/${"b".repeat(32)}/small`,
    ]);
    expect(faces(card("Checked by QA"))).toEqual([]);
    expect(faces(card("Waits"))).toEqual([]);
  });

  it("shows the member beside its verdicts and the messages sent to it", () => {
    show(
      project({ playbook: { ...codeTeam(), roles } }),
      {
        members: [ada, rune],
        tasks: [
          staffed({
            status: "reviewing",
            stage: "qa",
            revisions: [{ n: 1, brief_version: 2, files: [] }],
            verdicts: [
              {
                revision: 1,
                role: "Rune",
                brief_version: 2,
                outcome: "pass",
                summary: "Fine.",
              },
              {
                revision: 1,
                role: "QA",
                brief_version: 2,
                outcome: "revise",
                summary: "Fails.",
              },
            ],
            messages: [
              {
                id: "x1",
                to: "Ada",
                kind: "implementer",
                direction: 0,
                from: "owner",
                text: "Rename it.",
                status: "waiting",
              },
              {
                id: "x2",
                to: "QA",
                kind: "qa",
                direction: 0,
                from: "owner",
                text: "Run it again.",
                status: "waiting",
              },
            ],
          }),
        ],
      },
      { request: "t1" },
    );
    const chip = (text: string) =>
      screen.getByText(
        (_, el) =>
          el?.classList.contains("pill") === true && el.textContent === text,
      );
    expect(faces(chip("Rune: Passed"))).toEqual([
      `/api/avatars/${"b".repeat(32)}/small`,
    ]);
    expect(
      chip("Rune: Passed").querySelector("img")?.getAttribute("width"),
    ).toBe("16");
    expect(faces(chip("QA: Asked for changes"))).toEqual([]);
    const who = [...document.querySelectorAll<HTMLElement>(".thread-who")];
    expect(who.map((w) => w.textContent)).toEqual(["You → Ada", "You → QA"]);
    expect(faces(who[0])).toEqual([`/api/avatars/${"a".repeat(32)}/small`]);
    expect(faces(who[1])).toEqual([]);
  });
});

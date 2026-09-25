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
    expect(screen.queryByRole("link", { name: "Landing" })).toBeNull();
  });
  describe("its landed requests", () => {
    const landed = (id: string, objective: string, at: string) =>
      started({
        id,
        objective,
        status: "landed",
        stage: "done",
        updated_at: at,
        revisions: [
          { n: 1, brief_version: 2, files: [], ref: `${id}c0ffee12345` },
        ],
      });
    const tasks = () => [
      task({ id: "t1", objective: "Queued" }),
      landed("a1", "Older", "2026-09-21T10:00:00Z"),
      landed("b2", "Newest", "2026-09-23T10:00:00Z"),
      started({
        id: "s1",
        objective: "Called off",
        status: "stopped",
        stage: "stopped",
      }),
      landed("c3", "Middle", "2026-09-22T10:00:00Z"),
    ];
    const region = (name: string) =>
      screen.getByText(name).closest("details") as HTMLDetailsElement;
    const state = (list: Task[]) =>
      normalizeState({
        assistant: { name: "Iris", personality: "" },
        projects: [project()],
        tasks: list,
      });

    it("keeps them out of the columns, folded away with a count, apart from stopped ones", () => {
      show(project(), { tasks: tasks() });
      const columns = screen
        .getAllByRole("listitem")
        .filter((c) => c.classList.contains("board-column"));
      expect(columns.map((c) => c.getAttribute("aria-label"))).not.toContain(
        "Landed",
      );
      for (const c of columns)
        for (const name of ["Older", "Newest", "Middle", "Called off"])
          expect(within(c).queryByText(name)).toBeNull();
      const done = region("Landed (3)");
      expect(done.open).toBe(false);
      expect(done.classList.contains("landed")).toBe(true);
      expect(within(done).queryByText("Called off")).toBeNull();
      const stopped = region("Stopped (1)");
      expect(within(stopped).queryByText(/Older|Newest|Middle/)).toBeNull();
      expect(within(stopped).getByText("Called off")).toBeTruthy();
    });
    it("opens and folds, listing the most recently landed first", () => {
      show(project(), { tasks: tasks() });
      const done = region("Landed (3)");
      fireEvent.click(screen.getByText("Landed (3)"));
      expect(done.open).toBe(true);
      const links = within(done).getAllByRole("link");
      expect(links.map((a) => a.firstChild?.textContent)).toEqual([
        "Newest",
        "Middle",
        "Older",
      ]);
      expect(links[0].getAttribute("href")).toBe("#/projects/p1/requests/b2");
      expect(within(links[0]).getByText("b2c0ffe")).toBeTruthy();
      fireEvent.click(screen.getByText("Landed (3)"));
      expect(done.open).toBe(false);
    });
    it("calls them delivered for work that isn't code", () => {
      show(project({ playbook: writingTeam }), {
        tasks: [
          task({
            id: "w1",
            objective: "The note",
            status: "delivered",
            stage: "done",
          }),
        ],
      });
      expect(region("Delivered (1)")).toBeTruthy();
      expect(screen.queryByText(/Landed/)).toBeNull();
    });
    it("opens a landed request as before", () => {
      show(project(), { tasks: tasks() }, { request: "b2" });
      const panel = screen.getByRole("complementary", { name: "Newest" });
      expect(
        within(panel).getByText(
          "This request is finished. Ask for a new one to change it.",
        ),
      ).toBeTruthy();
    });
    it("stays open across a refresh, leaving an open request and focus alone", () => {
      window.history.replaceState(null, "", "/#/projects/p1/requests/b2");
      const view = show(project(), { tasks: tasks() }, { request: "b2" });
      const panel = screen.getByRole("complementary", { name: "Newest" });
      fireEvent.click(screen.getByText("Landed (3)"));
      const field = document.body.appendChild(
        document.createElement("textarea"),
      );
      try {
        field.focus();
        field.value = "a draft";
        view.rerender(
          <ProjectPage
            project={project()}
            route={{ page: "project", id: "p1", tab: "board", request: "b2" }}
            state={state([
              ...tasks(),
              landed("d4", "Just landed", "2026-09-24T10:00:00Z"),
            ])}
            refresh={refresh}
          />,
        );
        const done = region("Landed (4)");
        expect(done.open).toBe(true);
        expect(
          within(done).getAllByRole("link")[0].firstChild?.textContent,
        ).toBe("Just landed");
        expect(document.activeElement).toBe(field);
        expect(field.value).toBe("a draft");
        expect(panel.isConnected).toBe(true);
        expect(window.location.hash).toBe("#/projects/p1/requests/b2");
      } finally {
        field.remove();
      }
    });
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
    fireEvent.change(within(team).getByLabelText("QA runs"), {
      target: { value: "make check" },
    });
    // Where the work happens is set on the Config tab.
    for (const moved of [
      "Repository",
      "Branch prefix",
      /^Ignored folders to copy in/,
      "Sign commits",
    ])
      expect(within(team).queryByLabelText(moved)).toBeNull();
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
          repo: "",
          branch_prefix: "",
          check: "make check",
          prepare: [],
          sign: "",
        },
      },
    ]);
  });
  it("keeps where a code team works when its team is saved again", async () => {
    show(
      project({
        playbook: { ...codeTeam(), prepare: ["vendor"], sign: "always" },
      }),
      {},
      { tab: "team" },
    );
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const team = screen.getByRole("form", { name: "Team" });
    fireEvent.change(within(team).getByLabelText("Rounds before asking you"), {
      target: { value: "5" },
    });
    fireEvent.click(within(team).getByRole("button", { name: "Save team" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()[0].body).toMatchObject({
      max_rounds: "5",
      repo: "/work/service",
      branch_prefix: "paul/",
      check: "make check",
      prepare: ["vendor"],
      sign: "always",
    });
  });
  it("describes a code team by its roles and its check, leaving where it works to Config", () => {
    show(project(), {}, { tab: "team" });
    const team = screen.getByRole("region", { name: "Team" });
    expect(within(team).getByText("make check")).toBeTruthy();
    expect(
      within(team)
        .getAllByRole("listitem")
        .map((li) => li.querySelector(".seat-role")?.textContent),
    ).toEqual(["Planner", "Implementer", "Reviewer", "QA", "PM"]);
    expect(within(team).queryByText("/work/service")).toBeNull();
    expect(
      within(team).queryByText("Signed as your git config says"),
    ).toBeNull();
    expect(screen.queryByRole("region", { name: "Folders" })).toBeNull();
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
    expect(seat("Implementer").querySelector(".seat-who")?.textContent).toBe(
      "Ada",
    );
    expect(within(team).queryByRole("link", { name: "Reviewer" })).toBeNull();
  });
  const seat = (name: string) =>
    within(screen.getByRole("list", { name: "Roles" }))
      .getAllByRole("listitem")
      .find((li) => li.querySelector(".seat-role")?.textContent === name)!;
  it("lists who fills each role, offering only members of the right kind", () => {
    show(staffed(), { members: crew }, { tab: "team" });
    expect(
      within(seat("Implementer")).getByLabelText("Assign Implementer"),
    ).toHaveProperty("value", "m1");
    const reviewer = within(seat("Reviewer")).getByLabelText("Assign Reviewer");
    expect(
      within(reviewer)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["Template default", "Rune"]);
    expect(
      within(seat("Reviewer")).getByText("Template default · Codex"),
    ).toBeTruthy();
    expect(
      within(seat("Reviewer")).queryByRole("button", {
        name: "Unassign Reviewer",
      }),
    ).toBeNull();
  });
  const seatWrite = async (kind: string, member: string) => {
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: `/api/projects/p1/team/${kind}`,
        method: "PUT",
        body: { member },
      },
    ]);
  };
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
    const chooser = (p: Project) => {
      show(p, { members: planners }, { tab: "team" });
      return within(seat("Planner")).getByLabelText("Assign Planner");
    };
    it("shows a seat that holds several roles in each of them", () => {
      show(seats(ada, reviewer, qa), { members: planners }, { tab: "team" });
      for (const role of ["Planner", "Implementer"]) {
        expect(
          within(seat(role)).getByRole("link", { name: "Ada" }),
        ).toBeTruthy();
        expect(
          within(seat(role)).getByText("· Planner and implementer · Claude"),
        ).toBeTruthy();
      }
    });
    it("offers the template's planner, no planning, or a member who plans", async () => {
      const who = chooser(seats(ada, reviewer, qa));
      expect(who).toHaveProperty("value", "m1");
      expect(
        within(who)
          .getAllByRole("option")
          .map((o) => o.textContent),
      ).toEqual(["Template default", "No planning", "Ada Lovelace", "Pia"]);
      fireEvent.change(who, { target: { value: "m4" } });
      await seatWrite("planner", "m4");
    });
    it("shows a team without planning that way, and can bring the template's back", async () => {
      const who = chooser(seats(codeTeam().roles[0], reviewer, qa));
      expect(who).toHaveProperty("value", "none");
      expect(seat("Planner").querySelector(".seat-who")?.textContent).toBe(
        "No planning",
      );
      fireEvent.change(who, { target: { value: "" } });
      await seatWrite("planner", "");
    });
    it("leaves planning out of a team that has the template's planner", async () => {
      const who = chooser(seats(planner, codeTeam().roles[0], reviewer, qa));
      expect(who).toHaveProperty("value", "");
      fireEvent.change(who, { target: { value: "none" } });
      await seatWrite("planner", "none");
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
    it("chooses a PM from the members who hold it", async () => {
      show(project(), { members: crewWithPM }, { tab: "team" });
      expect(seat("PM").querySelector(".seat-who")?.textContent).toBe("No PM");
      const who = within(seat("PM")).getByLabelText("Assign PM");
      expect(who).toHaveProperty("value", "");
      expect(
        within(who)
          .getAllByRole("option")
          .map((o) => o.textContent),
      ).toEqual(["No PM", "Ada Lovelace", "Pia"]);
      fireEvent.change(who, { target: { value: "m4" } });
      await seatWrite("pm", "m4");
    });
    it("unassigns the PM, for writing too", async () => {
      show(
        project({
          playbook: { ...writingTeam, roles: [...writingTeam.roles, pia] },
        }),
        { members: crewWithPM },
        { tab: "team" },
      );
      expect(
        within(seat("PM")).getByRole("link", { name: "Pia" }),
      ).toBeTruthy();
      fireEvent.click(
        within(seat("PM")).getByRole("button", { name: "Unassign PM" }),
      );
      await seatWrite("pm", "");
    });
    it("keeps the PM and the planner when the team is saved again", async () => {
      show(
        project({
          playbook: { ...codeTeam(), roles: [...codeTeam().roles, pia] },
        }),
        { members: crewWithPM },
        { tab: "team" },
      );
      fireEvent.click(screen.getByRole("button", { name: "Edit" }));
      fireEvent.click(screen.getByRole("button", { name: "Save team" }));
      await waitFor(() => expect(refresh).toHaveBeenCalled());
      expect(writes()[0].body).toMatchObject({
        pm_member: "m4",
        planner_member: "none",
      });
    });
    it("offers no one while no member holds it", () => {
      show(project(), { members: crew }, { tab: "team" });
      expect(within(seat("PM")).getByText("No one to assign")).toBeTruthy();
    });
  });
  it("assigns a member to one role, sending nothing about the others", async () => {
    show(staffed(), { members: crew }, { tab: "team" });
    fireEvent.change(
      within(seat("Reviewer")).getByLabelText("Assign Reviewer"),
      { target: { value: "m2" } },
    );
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/projects/p1/team/reviewer",
        method: "PUT",
        body: { member: "m2" },
      },
    ]);
  });
  it("unassigns a member, giving the role back to the template", async () => {
    show(staffed(), { members: crew }, { tab: "team" });
    fireEvent.click(
      within(seat("Implementer")).getByRole("button", {
        name: "Unassign Implementer",
      }),
    );
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/projects/p1/team/implementer",
        method: "PUT",
        body: { member: "" },
      },
    ]);
  });
  it("keeps showing a member whose kinds changed since they were given the role", async () => {
    const changed = [
      member("m1", "Ada Lovelace", "reviewer"),
      ...crew.slice(1),
    ];
    show(staffed(), { members: changed }, { tab: "team" });
    const implementer = seat("Implementer");
    expect(within(implementer).getByRole("link", { name: "Ada" })).toBeTruthy();
    expect(
      within(implementer).getByText(/Kept here; no longer holds this role/),
    ).toBeTruthy();
    const who = within(implementer).getByLabelText("Assign Implementer");
    expect(who).toHaveProperty("value", "m1");
    expect(
      within(who).getByRole("option", { name: "Ada Lovelace" }),
    ).toHaveProperty("disabled", true);
    expect(
      within(implementer).getByRole("button", { name: "Unassign Implementer" }),
    ).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    fireEvent.click(screen.getByRole("button", { name: "Save team" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()[0].body).toMatchObject({ implementer_member: "m1" });
  });
  it("offers to unassign a member who has left, and never shows them as filling the role", () => {
    show(staffed(), { members: crew.slice(1) }, { tab: "team" });
    const implementer = seat("Implementer");
    expect(
      within(implementer).getByText("Template default · Claude"),
    ).toBeTruthy();
    expect(within(implementer).queryByRole("link")).toBeNull();
    expect(within(implementer).getByText("No one to assign")).toBeTruthy();
    expect(
      within(implementer).getByRole("button", { name: "Unassign Implementer" }),
    ).toBeTruthy();
  });
  it("offers no QA for a writing team and asks the engine only of roles no member fills", async () => {
    show(
      project({ playbook: writingTeam }),
      { members: crew },
      { tab: "team" },
    );
    expect(seat("Writer")).toBeTruthy();
    expect(seat("QA")).toBeUndefined();
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const team = screen.getByRole("form", { name: "Team" });
    expect(within(team).getByRole("group", { name: "Writer" })).toBeTruthy();
    expect(within(team).queryByRole("group", { name: "QA" })).toBeNull();
    expect(within(team).queryByRole("group", { name: "Planner" })).toBeNull();
    fireEvent.click(within(team).getByRole("button", { name: "Save team" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()[0].body).toMatchObject({
      implementer_member: "",
      reviewer_member: "",
      qa_member: "",
      planner_member: "",
      pm_member: "",
    });
    cleanup();
    show(staffed(), { members: crew }, { tab: "team" });
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    expect(screen.queryByRole("group", { name: "Implementer" })).toBeNull();
    expect(screen.getAllByLabelText("Engine")).toHaveLength(1);
  });
  it("is called Config, and holds where the work happens and its folders", () => {
    show(project(), {}, { tab: "config" });
    const tab = screen.getByRole("link", { name: "Config" });
    expect(tab.getAttribute("href")).toBe("#/projects/p1/config");
    expect(tab.getAttribute("aria-current")).toBe("page");
    expect(screen.queryByRole("link", { name: "Landing" })).toBeNull();
    expect(screen.getByRole("region", { name: "Landing" })).toBeTruthy();
    const workspace = screen.getByRole("region", { name: "Workspace" });
    expect(within(workspace).getByText("/work/service")).toBeTruthy();
    expect(within(workspace).getByText("paul/")).toBeTruthy();
    expect(
      within(workspace).getByText("Signed as your git config says"),
    ).toBeTruthy();
    expect(screen.getByRole("region", { name: "Folders" })).toBeTruthy();
  });
  it("saves only where a code team works from Config", async () => {
    show(
      project({
        playbook: {
          ...codeTeam(),
          roles: staffed().playbook!.roles,
          max_rounds: 4,
          prepare: ["vendor"],
        },
      }),
      { members: crew },
      { tab: "config" },
    );
    const workspace = screen.getByRole("region", { name: "Workspace" });
    fireEvent.click(within(workspace).getByRole("button", { name: "Edit" }));
    const form = screen.getByRole("form", { name: "Workspace" });
    expect(within(form).getByLabelText("Repository")).toHaveProperty(
      "value",
      "/work/service",
    );
    expect(
      within(form).getByLabelText(/^Ignored folders to copy in/),
    ).toHaveProperty("value", "vendor");
    fireEvent.change(within(form).getByLabelText("Repository"), {
      target: { value: "/work/notes" },
    });
    fireEvent.change(within(form).getByLabelText("Branch prefix"), {
      target: { value: " crew/ " },
    });
    fireEvent.change(
      within(form).getByLabelText(/^Ignored folders to copy in/),
      { target: { value: "ui/node_modules, vendor" } },
    );
    fireEvent.change(within(form).getByLabelText("Sign commits"), {
      target: { value: "never" },
    });
    fireEvent.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/projects/p1/workspace",
        method: "PUT",
        body: {
          repo: "/work/notes",
          branch_prefix: "crew/",
          prepare: ["ui/node_modules", "vendor"],
          sign: "never",
        },
      },
    ]);
  });
  it("gives a writing project's Config only its folders", () => {
    show(project({ playbook: writingTeam }), {}, { tab: "config" });
    expect(screen.getByRole("region", { name: "Folders" })).toBeTruthy();
    expect(screen.queryByRole("region", { name: "Landing" })).toBeNull();
    expect(screen.queryByRole("region", { name: "Workspace" })).toBeNull();
  });
  it("sets where a code team's changes land, on the Config tab", async () => {
    show(project({ playbook: codeTeam({}) }), {}, { tab: "config" });
    const landing = screen.getByRole("region", { name: "Landing" });
    expect(within(landing).getByText("A new local branch")).toBeTruthy();
    expect(within(landing).getByText("Undoable")).toBeTruthy();
    fireEvent.click(within(landing).getByRole("button", { name: "Edit" }));
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

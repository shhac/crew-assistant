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
  type Playbook,
  type Project,
  type State,
  type Task,
} from "./api";

const writingTeam: Playbook = {
  template: "draft",
  medium: "documents",
  roles: [
    { name: "Writer", kind: "implementer", engine: "claude" },
    { name: "Reviewer", kind: "reviewer", engine: "codex" },
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
    { name: "Implementer", kind: "implementer", engine: "claude" },
    { name: "Reviewer", kind: "reviewer", engine: "codex" },
    { name: "QA", kind: "qa", engine: "codex" },
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

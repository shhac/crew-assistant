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
import { ProjectDetail } from "./ProjectDetail";
import {
  normalizeState,
  type Decision,
  type LandPolicy,
  type Project,
  type State,
  type Task,
} from "./api";

const project = (overrides: Partial<Project> = {}): Project => ({
  id: "p1",
  title: "Launch note",
  status: "active",
  brief: {
    version: 2,
    goal: "Tell customers what changed",
    audience: "Existing customers",
    constraints: "Plain English",
    criteria: ["Under 300 words"],
    updated_at: "2026-09-20T10:00:00Z",
  },
  playbook: {
    template: "draft",
    medium: "documents",
    roles: [
      { name: "Writer", kind: "implementer", engine: "claude" },
      { name: "Reviewer", kind: "reviewer", engine: "codex" },
    ],
    max_rounds: 3,
    deliver: "owner",
    deliver_to: "/home/out",
  },
  directories: [],
  ...overrides,
});

const task = (overrides: Partial<Task> = {}): Task => ({
  id: "t1",
  project_id: "p1",
  objective: "Draft the launch note",
  criteria: [],
  status: "queued",
  round: 0,
  revisions: [],
  verdicts: [],
  created_at: "2026-09-21T10:00:00Z",
  ...overrides,
});

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
      const body = revision ? { files: files[Number(revision[1])] } : {};
      return { ok: true, status: 200, json: async () => body };
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function show(
  p: Project,
  extra: { tasks?: Task[]; decisions?: Decision[] } = {},
) {
  const state: State = normalizeState({
    assistant: { name: "Iris", personality: "" },
    projects: [p],
    ...extra,
  });
  return render(
    <ProjectDetail
      project={p}
      state={state}
      onBack={vi.fn()}
      refresh={refresh}
    />,
  );
}

const writes = () => calls.filter((c) => c.method !== "GET");

describe("project page", () => {
  it("shows the brief and saves an edit as a new version", async () => {
    show(project());
    const brief = screen.getByRole("region", { name: "Brief" });
    expect(within(brief).getByText("Tell customers what changed")).toBeTruthy();
    expect(within(brief).getByText("Existing customers")).toBeTruthy();
    expect(within(brief).getByText("Plain English")).toBeTruthy();
    expect(within(brief).getByText("Under 300 words")).toBeTruthy();
    expect(within(brief).getByText(/Version 2/)).toBeTruthy();

    fireEvent.click(within(brief).getByRole("button", { name: "Edit brief" }));
    fireEvent.change(within(brief).getByLabelText("Goal"), {
      target: { value: "Tell every customer what changed" },
    });
    fireEvent.change(
      within(brief).getByLabelText("What does done look like?"),
      {
        target: { value: "Under 300 words\nOne call to action" },
      },
    );
    fireEvent.click(within(brief).getByRole("button", { name: "Save brief" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/projects/p1/brief",
        method: "PUT",
        body: {
          goal: "Tell every customer what changed",
          audience: "Existing customers",
          constraints: "Plain English",
          criteria: ["Under 300 words", "One call to action"],
        },
      },
    ]);
  });

  it("asks for something and clears the form", async () => {
    show(project());
    const ask = screen.getByRole("form", { name: "Ask for something" });
    fireEvent.change(within(ask).getByLabelText("What do you want?"), {
      target: { value: "A short launch note" },
    });
    fireEvent.change(
      within(ask).getByLabelText("How will you judge it? (optional)"),
      { target: { value: "Mentions pricing\n" } },
    );
    fireEvent.click(within(ask).getByRole("button", { name: "Ask" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/projects/p1/tasks",
        method: "POST",
        body: {
          objective: "A short launch note",
          criteria: ["Mentions pricing"],
        },
      },
    ]);
    expect(within(ask).getByLabelText("What do you want?")).toHaveProperty(
      "value",
      "",
    );
  });

  it("explains why it can't take requests without a team, and offers one", async () => {
    show(project({ playbook: undefined }));
    const ask = screen.getByRole("form", { name: "Ask for something" });
    expect(within(ask).getByText(/Choose a team first/)).toBeTruthy();
    expect(
      within(ask).getByLabelText("What do you want?").matches(":disabled"),
    ).toBe(true);
    const team = screen.getByRole("region", { name: "Team" });
    fireEvent.click(
      within(team).getByRole("button", { name: "Choose a team" }),
    );
    fireEvent.change(within(team).getByLabelText("Reviewer"), {
      target: { value: "claude" },
    });
    fireEvent.change(
      within(team).getByLabelText("Rounds before checking with you"),
      { target: { value: "2" } },
    );
    fireEvent.click(within(team).getByRole("button", { name: "Save team" }));
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      {
        path: "/api/projects/p1/team",
        method: "PUT",
        body: {
          template: "draft",
          writer_engine: "claude",
          reviewer_engine: "claude",
          max_rounds: "2",
          deliver_to: "",
        },
      },
    ]);
  });

  it("sets up a code team on one of the project's folders", async () => {
    show(
      project({
        playbook: undefined,
        directories: ["/work/service", "/work/notes"],
      }),
    );
    const team = screen.getByRole("region", { name: "Team" });
    fireEvent.click(
      within(team).getByRole("button", { name: "Choose a team" }),
    );
    expect(within(team).queryByLabelText("Repository")).toBeNull();
    fireEvent.change(within(team).getByLabelText("Kind of work"), {
      target: { value: "code" },
    });
    expect(within(team).getByLabelText("Implementer")).toBeTruthy();
    expect(within(team).queryByText("Deliver approved work to")).toBeNull();
    fireEvent.change(within(team).getByLabelText("Repository"), {
      target: { value: "/work/service" },
    });
    fireEvent.change(within(team).getByLabelText("Check QA runs"), {
      target: { value: "make check" },
    });
    fireEvent.change(within(team).getByLabelText("Branch prefix"), {
      target: { value: "paul/" },
    });
    fireEvent.change(
      within(team).getByLabelText("Ignored folders to copy in (optional)"),
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

  it("describes a code team by where it works and what it delivers", () => {
    show(
      project({
        playbook: {
          template: "code",
          medium: "git",
          roles: [
            { name: "Implementer", kind: "implementer", engine: "claude" },
            { name: "Reviewer", kind: "reviewer", engine: "codex" },
            { name: "QA", kind: "qa", engine: "codex" },
          ],
          max_rounds: 3,
          deliver: "approve",
          repo: "/work/service",
          branch_prefix: "paul/",
          check: "make check",
        },
      }),
    );
    const team = screen.getByRole("region", { name: "Team" });
    expect(within(team).getByText("/work/service")).toBeTruthy();
    expect(within(team).getByText("make check")).toBeTruthy();
    expect(within(team).getByText(/nothing is pushed/)).toBeTruthy();
  });

  const codeProject = (land: LandPolicy) =>
    project({
      playbook: {
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
      },
    });

  it("sets where a code team's changes land", async () => {
    show(codeProject({}));
    const team = screen.getByRole("region", { name: "Team" });
    expect(within(team).getByText(/become a local branch/)).toBeTruthy();
    fireEvent.click(
      within(team).getByRole("button", { name: "Change where changes land" }),
    );
    fireEvent.change(within(team).getByLabelText("When approved"), {
      target: { value: "push" },
    });
    fireEvent.change(
      within(team).getByLabelText("What landing means here (optional)"),
      { target: { value: "fully ff-merged to main" } },
    );
    fireEvent.click(within(team).getByRole("button", { name: "Save" }));
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

  it("offers to land a change delivered before the project landed on main", async () => {
    show(
      codeProject({
        via: "push",
        target: "main",
        means: "fully ff-merged to main",
      }),
      {
        tasks: [
          task({ id: "t1", status: "delivered", delivered_to: "paul/feature" }),
          task({
            id: "t2",
            status: "landed",
            delivered_to: "main",
            objective: "Earlier",
          }),
        ],
      },
    );
    expect(screen.getByText(/Landing means:/)).toBeTruthy();
    expect(screen.getByText("On main")).toBeTruthy();
    const buttons = screen.getAllByRole("button", { name: "Land on main" });
    expect(buttons).toHaveLength(1);
    fireEvent.click(buttons[0]);
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      { path: "/api/projects/p1/tasks/t1/land", method: "POST", body: {} },
    ]);
  });

  it("explains why it can't take requests without a brief", () => {
    show(project({ brief: { version: 0, goal: "", criteria: null } }));
    expect(screen.getByText(/Write the brief first/)).toBeTruthy();
    expect(screen.getByText(/No brief yet\. A brief says/)).toBeTruthy();
  });

  it("describes each request's status in plain words, newest first", () => {
    const tasks = [
      task({
        id: "a",
        objective: "Oldest",
        status: "delivered",
        delivered_to: "/home/out/note.md",
        created_at: "2026-09-01T00:00:00Z",
      }),
      task({
        id: "b",
        objective: "Stopped one",
        status: "stopped",
        detail: "the owner stopped it",
        created_at: "2026-09-02T00:00:00Z",
      }),
      task({
        id: "c",
        objective: "Writing one",
        status: "writing",
        revisions: [{ n: 1, brief_version: 2, files: [] }],
        created_at: "2026-09-03T00:00:00Z",
      }),
      task({
        id: "d",
        objective: "Newest",
        status: "queued",
        created_at: "2026-09-04T00:00:00Z",
      }),
    ];
    show(project(), { tasks });
    const rows = within(screen.getByRole("list", { name: "Requests" }))
      .getAllByRole("listitem")
      .map((li) => li.textContent);
    expect(rows).toEqual([
      "NewestStopWaiting to start",
      "Writing oneStopWriting draft 2",
      "Stopped onethe owner stopped itStopped",
      "OldestDelivered to /home/out/note.mdDelivered",
    ]);
  });

  it("stops an unfinished request and offers nothing for finished ones", async () => {
    show(project(), {
      tasks: [
        task({ id: "t1", status: "writing", round: 1 }),
        task({ id: "t2", status: "delivered", objective: "Old note" }),
      ],
    });
    const list = screen.getByRole("list", { name: "Requests" });
    const stops = within(list).getAllByRole("button", { name: "Stop" });
    expect(stops).toHaveLength(1);
    fireEvent.click(stops[0]);
    await waitFor(() => expect(refresh).toHaveBeenCalled());
    expect(writes()).toEqual([
      { path: "/api/projects/p1/tasks/t1/stop", method: "POST", body: {} },
    ]);
  });

  it("links a waiting request to its decision", () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    show(project(), {
      tasks: [task({ status: "waiting", decision_id: "d1" })],
      decisions: [
        {
          id: "d1",
          project_id: "p1",
          task_id: "t1",
          kind: "question",
          title: "Reviewer has a question",
          context: "Which pricing tier?",
          recommendation: "Answer the question",
          choices: ["Use your judgement", "Stop"],
          status: "open",
        },
      ],
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Needs your decision" }),
    );
    expect(scroll).toHaveBeenCalled();
    expect(document.getElementById("decision-d1")).toBeTruthy();
  });

  it("previews the latest draft and can switch to an earlier one", async () => {
    files = {
      2: [
        { path: "note.md", content: "# Launch\n\nNew **pricing**.", size: 30 },
        { path: "cover.png", binary: true, size: 2048 },
      ],
      1: [
        {
          path: "note.txt",
          content: "first try",
          truncated: true,
          size: 999999,
        },
      ],
    };
    show(project(), {
      tasks: [
        task({
          status: "waiting",
          revisions: [
            { n: 1, brief_version: 2, files: ["note.txt"] },
            { n: 2, brief_version: 2, files: ["note.md", "cover.png"] },
          ],
        }),
      ],
    });
    const deliverable = screen.getByRole("region", {
      name: "Latest deliverable",
    });
    expect(
      await within(deliverable).findByRole("heading", { name: "Launch" }),
    ).toBeTruthy();
    expect(within(deliverable).getByText("pricing").tagName).toBe("STRONG");
    expect(
      within(deliverable).getByText(/cover\.png is not text/),
    ).toBeTruthy();
    expect(calls.at(-1)?.path).toBe("/api/projects/p1/tasks/t1/revisions/2");

    fireEvent.change(within(deliverable).getByLabelText("Draft"), {
      target: { value: "1" },
    });
    expect(await within(deliverable).findByText("first try")).toBeTruthy();
    expect(
      within(deliverable).getByText(/Only the beginning of note\.txt/),
    ).toBeTruthy();
    expect(calls.at(-1)?.path).toBe("/api/projects/p1/tasks/t1/revisions/1");
  });

  it("keeps rounds and reviews collapsed until asked", () => {
    show(project(), {
      tasks: [
        task({
          status: "writing",
          direction: ["Mention the new tier"],
          revisions: [
            { n: 1, brief_version: 1, files: [], summary: "First pass" },
          ],
          verdicts: [
            {
              revision: 1,
              role: "Reviewer",
              brief_version: 1,
              outcome: "revise",
              summary: "Too long",
              findings: [
                { criterion: "Under 300 words", note: "It is 420 words" },
              ],
            },
          ],
        }),
      ],
    });
    const drill = screen.getByText("Rounds and reviews").parentElement!;
    expect((drill as HTMLDetailsElement).open).toBe(false);
    expect(within(drill).getByText("First pass")).toBeTruthy();
    expect(
      within(drill).getByText("Written against brief version 1"),
    ).toBeTruthy();
    expect(
      within(drill).getByText("Asked for changes", { exact: false }),
    ).toBeTruthy();
    expect(within(drill).getByText("It is 420 words")).toBeTruthy();
    expect(within(drill).getByText("Mention the new tier")).toBeTruthy();
  });

  describe("delivery decisions", () => {
    const delivery: Decision = {
      id: "d9",
      project_id: "p1",
      task_id: "t1",
      kind: "delivery",
      title: "Draft 2 of Draft the launch note is ready",
      context: "Reviews passed.",
      recommendation: "Approve",
      choices: ["Approve", "Request changes"],
      status: "open",
    };

    it("approves with one click", async () => {
      show(project(), { decisions: [delivery] });
      const card = screen
        .getByRole("heading", { name: delivery.title })
        .closest("article")!;
      expect(
        within(card).queryByRole("button", { name: "Give a different answer" }),
      ).toBeNull();
      const approve = within(card).getByRole("button", { name: "Approve" });
      expect(approve.className).toContain("warm");
      fireEvent.click(approve);
      await waitFor(() => expect(refresh).toHaveBeenCalled());
      expect(writes()).toEqual([
        {
          path: "/api/decisions/d9/resolve",
          method: "POST",
          body: { choice: "Approve" },
        },
      ]);
    });

    it("asks what should change and sends it as the answer", async () => {
      show(project(), { decisions: [delivery] });
      const card = screen
        .getByRole("heading", { name: delivery.title })
        .closest("article")!;
      fireEvent.click(
        within(card).getByRole("button", { name: "Request changes" }),
      );
      expect(writes()).toEqual([]);
      fireEvent.change(within(card).getByLabelText("What should change?"), {
        target: { value: "Lead with the price change" },
      });
      fireEvent.click(
        within(card).getByRole("button", { name: "Send changes" }),
      );
      await waitFor(() => expect(refresh).toHaveBeenCalled());
      expect(writes()).toEqual([
        {
          path: "/api/decisions/d9/resolve",
          method: "POST",
          body: { answer: "Lead with the price change" },
        },
      ]);
    });
  });
});

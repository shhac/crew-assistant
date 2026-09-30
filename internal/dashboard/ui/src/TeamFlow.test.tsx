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
import { TeamFlow } from "./TeamThread";
import { MemberPanel } from "./MemberPanel";
import { stageFlow } from "./taskFlow";
import { normalizeState, type Project, type Role, type Task } from "./api";

const roles: Role[] = [
  {
    name: "Ash",
    kinds: ["researcher", "implementer"],
    engine: "claude",
    model: "opus",
    member: "a",
  },
  {
    name: "Rune",
    kinds: ["reviewer"],
    engine: "codex",
    model: "example-model",
  },
  { name: "Quinn", kinds: ["qa"], engine: "codex" },
];
const project: Project = {
  id: "p",
  title: "Example",
  status: "active",
  brief: { version: 1, goal: "Example", criteria: [] },
  playbook: {
    template: "code",
    medium: "git",
    roles,
    deliver: "owner",
    max_rounds: 3,
  },
};
const task = (overrides: Partial<Task> = {}): Task => ({
  id: "t",
  project_id: "p",
  objective: "Example",
  criteria: [],
  status: "queued",
  stage: "todo",
  roles,
  round: 1,
  revisions: [],
  verdicts: [],
  ...overrides,
});
const state = normalizeState({
  members: [
    {
      id: "a",
      name: "Ash",
      kinds: ["researcher", "implementer"],
      engine: "claude",
      model: "opus",
      learnings: [],
    },
  ],
});
const onOpen = vi.fn();
const show = (t = task(), s = state) =>
  render(<TeamFlow project={project} task={t} state={s} onOpen={onOpen} />);
const stage = (kind: string) =>
  document.querySelector<HTMLButtonElement>(
    `button[data-stage="${kind}:${kind === "reviewer" ? "Rune" : kind === "qa" ? "Quinn" : "Ash"}"]`,
  )!;
beforeEach(() => {
  onOpen.mockClear();
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({ ok: true, json: async () => ({ steps: [] }) })),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("the task stage flow", () => {
  it("keeps an idle second implementer's waiting messages visible", () => {
    const second: Role = {
      name: "Ash #2",
      kinds: ["implementer"],
      engine: "codex",
    };
    show(
      task({
        status: "writing",
        stage: "implementing",
        roles: [...roles, second],
        messages: [
          {
            id: "m",
            to: second.name,
            kind: "implementer",
            from: "owner",
            text: "Check",
            direction: 0,
            status: "waiting",
          },
        ],
      }),
    );
    const idle = document.querySelector<HTMLElement>(
      'button[data-stage="implementer:Ash #2"]',
    )!;
    expect(within(idle).getByText("Not started")).toBeTruthy();
    expect(within(idle).getByText("1 message waiting")).toBeTruthy();
    fireEvent.click(idle);
    expect(onOpen).toHaveBeenCalledWith("Ash #2", "implementer:Ash #2");
  });
  it("puts a long block reason in content, leaving a short exception pill", () => {
    const reason =
      "A long reason explaining the missing fixture. More details belong below the member rather than inside a pill.";
    show(
      task({ status: "waiting", stage: "implementing", decision_id: "d" }),
      normalizeState({
        decisions: [
          {
            id: "d",
            kind: "failure",
            title: "Stuck",
            context: reason,
            recommendation: "",
            choices: [],
            status: "pending",
          },
        ],
      }),
    );
    const row = stage("implementer");
    expect(
      within(row.querySelector<HTMLElement>(".task-stage-content")!).getByText(
        reason,
      ),
    ).toBeTruthy();
    const meta = within(row.querySelector<HTMLElement>(".task-stage-meta")!);
    expect(meta.getByText("Blocked")).toBeTruthy();
    expect(meta.queryByText(reason)).toBeNull();
  });
  it("names pending stages and providers with a prerequisite, using stage order", () => {
    show();
    expect(
      [...document.querySelectorAll("button[data-stage]")].map((b) =>
        b.getAttribute("data-stage"),
      ),
    ).toEqual([
      "researcher:Ash",
      "implementer:Ash",
      "reviewer:Rune",
      "qa:Quinn",
    ]);
    const review = within(stage("reviewer"));
    expect(review.getByText("Not started")).toBeTruthy();
    expect(review.getByText("Rune")).toBeTruthy();
    expect(review.getByRole("img", { name: "Codex" })).toBeTruthy();
    expect(review.getByText("Reviewer · example-model")).toBeTruthy();
    expect(
      review.getByText("Rune will start when this request begins"),
    ).toBeTruthy();
    expect(screen.queryByText("Up next")).toBeNull();
    fireEvent.click(stage("reviewer"));
    expect(onOpen).toHaveBeenCalledWith("Rune", "reviewer:Rune");
  });
  it("keeps working, done and pending markers distinct and reveals earlier rounds", () => {
    show(
      task({
        status: "writing",
        stage: "implementing",
        round: 2,
        plan: { role: "Ash", summary: "Ready", at: "2026-09-29T10:00:00Z" },
        revisions: [
          { n: 1, brief_version: 1, files: [], summary: "First build" },
        ],
      }),
    );
    expect(within(stage("researcher")).getByText("Done")).toBeTruthy();
    expect(within(stage("implementer")).getByText("Working now")).toBeTruthy();
    expect(within(stage("reviewer")).getByText("Not started")).toBeTruthy();
    expect(
      stage("researcher").querySelector('[data-progress="done"] svg'),
    ).toBeTruthy();
    expect(
      stage("implementer").querySelector('[data-progress="working"] .dot'),
    ).toBeTruthy();
    expect(
      stage("reviewer").querySelector('[data-progress="not-started"] svg'),
    ).toBeTruthy();
    expect(
      within(stage("implementer")).getByText("Build · round 2"),
    ).toBeTruthy();
    expect(
      within(stage("reviewer")).getByText("Review · build 2"),
    ).toBeTruthy();
    const summary = screen.getByText("Previous work (1)");
    const details = summary.closest("details")!;
    expect(details.open).toBe(false);
    fireEvent.click(summary);
    expect(details.open).toBe(true);
    expect(within(details).getByText("Build 1 · First build")).toBeTruthy();
    expect(details.closest("button")).toBeNull();
  });
  it("keeps message counts beside progress and displays them only once for a repeated member", () => {
    show(
      task({
        status: "writing",
        stage: "implementing",
        messages: [
          {
            id: "m",
            to: "Ash",
            kind: "implementer",
            from: "owner",
            text: "Check",
            direction: 0,
            status: "waiting",
          },
        ],
      }),
    );
    const meta = stage("implementer").querySelector(".task-stage-meta")!;
    expect(within(meta as HTMLElement).getByText("Working now")).toBeTruthy();
    expect(
      within(meta as HTMLElement).getByText("1 message waiting"),
    ).toBeTruthy();
    expect(screen.getAllByText("1 message waiting")).toHaveLength(1);
  });
  it.each(["owner", "block", "stopped"])(
    "layers %s exceptions over a readable stage state",
    (exception) => {
      const t = task({
        status: exception === "stopped" ? "stopped" : "waiting",
        stage: "implementing",
        decision_id: "d",
        blockers:
          exception === "block"
            ? [
                {
                  id: "b",
                  kind: "manual",
                  description: "Missing fixture",
                  by: "owner",
                  at: "2026-09-29T10:00:00Z",
                },
              ]
            : [],
      });
      const s = normalizeState({
        decisions: [
          {
            id: "d",
            kind: "question",
            title: "Question",
            context: "",
            recommendation: "",
            choices: [],
            status: "pending",
          },
        ],
      });
      show(t, s);
      const row = within(stage("implementer"));
      expect(
        row.getByText(exception === "stopped" ? "Incomplete" : "Working now"),
      ).toBeTruthy();
      if (exception === "stopped") {
        expect(screen.getAllByText("Stopped")).toHaveLength(1);
        expect(screen.queryByText("Working now")).toBeNull();
        expect(stage("implementer").querySelector(".dot")).toBeNull();
        expect(
          stage("implementer").querySelector(".task-stage-marker svg"),
        ).toBeTruthy();
        expect(row.getByText("Incomplete").className).not.toContain(
          "tone-work",
        );
      }
      expect(
        row.getByText(
          exception === "owner"
            ? "Question for you"
            : exception === "block"
              ? "Missing fixture"
              : "Stopped",
        ),
      ).toBeTruthy();
    },
  );
  it("uses the pinned provider after a member is deleted", () => {
    show(task(), normalizeState({}));
    expect(
      within(stage("implementer")).getByRole("img", { name: "Claude" }),
    ).toBeTruthy();
    expect(stage("implementer").querySelector(".avatar")).toBeNull();
  });
});

describe("a pending member panel", () => {
  it.each(["Pia", "Ash #2", "Ash"])(
    "does not claim %s has no work while its steps are unknown",
    (seat) => {
      vi.stubGlobal(
        "fetch",
        vi.fn(() => new Promise(() => {})),
      );
      const extra: Role = {
        name: seat,
        kinds: [seat === "Pia" ? "pm" : "implementer"],
        engine: "codex",
      };
      render(
        <MemberPanel
          project={project}
          task={task({
            status: "reviewing",
            stage: "reviewing",
            revisions: [{ n: 1, brief_version: 1, files: [] }],
            roles: seat === "Ash" ? roles : [...roles, extra],
          })}
          seat={seat}
          state={state}
          onClose={() => {}}
          refresh={async () => {}}
        />,
      );
      expect(screen.queryByRole("region", { name: "Not started" })).toBeNull();
      expect(
        screen.queryByText(`No work from ${seat} on this request yet.`),
      ).toBeNull();
      expect(screen.getByText("Loading what they've done…")).toBeTruthy();
    },
  );
  it.each(["Pia", "Ash #2"])(
    "shows %s's steps fetch failure instead of a false pending card",
    async (seat) => {
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => {
          throw new Error("Steps unavailable");
        }),
      );
      const extra: Role = {
        name: seat,
        kinds: [seat === "Pia" ? "pm" : "implementer"],
        engine: "codex",
      };
      render(
        <MemberPanel
          project={project}
          task={task({ roles: [...roles, extra] })}
          seat={seat}
          state={state}
          onClose={() => {}}
          refresh={async () => {}}
        />,
      );
      await screen.findByText("Steps unavailable");
      expect(screen.queryByRole("region", { name: "Not started" })).toBeNull();
      expect(
        screen.queryByText(`No work from ${seat} on this request yet.`),
      ).toBeNull();
    },
  );
  it("keeps a researcher's earlier plan visible while a new pass is pending", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: true,
        json: async () => ({
          steps: [
            {
              id: "s",
              kind: "reply",
              role: "researcher",
              at: "2026-09-29T10:00:00Z",
              text: "Earlier research",
            },
          ],
        }),
      })),
    );
    const researcher: Role = {
      name: "Researcher",
      kinds: ["researcher"],
      engine: "codex",
    };
    const t = task({
      status: "waiting",
      stage: "reviewing",
      roles: [researcher, ...roles],
      plan: {
        role: "Researcher",
        summary: "Ready",
        at: "2026-09-29T10:00:00Z",
      },
      research: [
        {
          id: "r",
          from: "Rune",
          round: 1,
          revision: 1,
          question: "Look again",
        },
      ],
    });
    expect(
      stageFlow(t, project, state).find((r) => r.role?.name === "Researcher")
        ?.state,
    ).toBe("not-started");
    render(
      <MemberPanel
        project={project}
        task={t}
        seat="Researcher"
        state={state}
        onClose={() => {}}
        refresh={async () => {}}
      />,
    );
    await screen.findByText("Earlier research");
    expect(screen.queryByRole("region", { name: "Not started" })).toBeNull();
    expect(
      screen.queryByText("No work from Researcher on this request yet."),
    ).toBeNull();
  });
  it.each(["writing", "stopped", "researching"] as const)(
    "retains earlier work while %s",
    async (status) => {
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => ({
          ok: true,
          json: async () => ({
            steps: [
              {
                id: "s",
                kind: "reply",
                role: "reviewer",
                at: "2026-09-29T10:00:00Z",
                text: "Earlier work",
              },
            ],
          }),
        })),
      );
      const seat = status === "researching" ? "Ash" : "Rune";
      const t = task({
        status,
        stage: status === "writing" ? "implementing" : status,
        round: 2,
        revisions: [{ n: 1, brief_version: 1, files: [] }],
        verdicts: [
          {
            revision: 1,
            role: "Rune",
            brief_version: 1,
            outcome: "pass",
            summary: "Reviewed",
          },
        ],
        ...(status === "researching"
          ? {
              plan: {
                role: "Ash",
                summary: "Researched",
                at: "2026-09-29T10:00:00Z",
              },
              research: [
                {
                  id: "r",
                  from: "Rune",
                  round: 1,
                  revision: 1,
                  question: "Again",
                },
              ],
            }
          : {}),
      });
      render(
        <MemberPanel
          project={project}
          task={t}
          seat={seat}
          state={state}
          onClose={() => {}}
          refresh={async () => {}}
        />,
      );
      await waitFor(() =>
        expect(document.querySelector(".member-timeline")).toBeTruthy(),
      );
      expect(screen.queryByRole("region", { name: "Not started" })).toBeNull();
      expect(
        screen.queryByText(`No work from ${seat} on this request yet.`),
      ).toBeNull();
    },
  );
  it.each(["Pia", "Ash #2"])(
    "gives %s without a flow row an intentional panel",
    async (seat) => {
      const role: Role = {
        name: seat,
        kinds: [seat === "Pia" ? "pm" : "implementer"],
        engine: "codex",
      };
      render(
        <MemberPanel
          project={project}
          task={task({ roles: [...roles, role] })}
          seat={seat}
          state={state}
          onClose={() => {}}
          refresh={async () => {}}
        />,
      );
      expect(
        await screen.findByRole("region", { name: "Not started" }),
      ).toBeTruthy();
      expect(
        screen.getByText(`${seat} has no stage on this request.`),
      ).toBeTruthy();
      expect(document.querySelector(".member-timeline")).toBeNull();
      expect(
        screen.queryByText(/Not at work|Nothing from|last active/),
      ).toBeNull();
    },
  );
  it("states a shared prerequisite once when a seat holds several pending stages", () => {
    render(
      <MemberPanel
        project={project}
        task={task()}
        seat="Ash"
        state={state}
        onClose={() => {}}
        refresh={async () => {}}
      />,
    );
    expect(
      screen.getAllByText("Ash will start when this request begins"),
    ).toHaveLength(1);
    expect(
      screen.getByText("No work from Ash on this request yet."),
    ).toBeTruthy();
    expect(document.querySelector(".member-timeline")).toBeNull();
  });
  it("shows the steps error with provider identity and composer", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new Error("Steps unavailable");
      }),
    );
    render(
      <MemberPanel
        project={project}
        task={task({ status: "researching", stage: "researching" })}
        seat="Rune"
        state={state}
        onClose={() => {}}
        refresh={async () => {}}
      />,
    );
    expect(screen.getByRole("region", { name: "Not started" })).toBeTruthy();
    expect(
      screen.getByText("Rune will check build 1 when it is ready"),
    ).toBeTruthy();
    expect(
      screen.getByText("No work from Rune on this request yet."),
    ).toBeTruthy();
    expect(screen.getByRole("img", { name: "Codex" })).toBeTruthy();
    expect(screen.getByText("Reviewer · example-model")).toBeTruthy();
    expect(screen.queryByText("Codex")).toBeNull();
    expect(screen.getByLabelText("Message")).toBeTruthy();
    await screen.findByText("Steps unavailable");
    expect(screen.getByRole("region", { name: "Not started" })).toBeTruthy();
    expect(
      screen.queryByText(/last active|Not at work|Loading what|Nothing from/),
    ).toBeNull();
    expect(document.querySelector(".member-timeline")).toBeNull();
  });
  it("keeps the read-only explanation for pending research seats", () => {
    const researcher = {
      name: "Researcher",
      kinds: ["researcher"],
      engine: "codex",
    };
    render(
      <MemberPanel
        project={project}
        task={task({ roles: [researcher, ...roles.slice(1)] })}
        seat="Researcher"
        state={state}
        onClose={() => {}}
        refresh={async () => {}}
      />,
    );
    expect(
      screen.getByText("No work from Researcher on this request yet."),
    ).toBeTruthy();
    expect(
      screen.getByText(
        "Only the seats that write and check the work take messages.",
      ),
    ).toBeTruthy();
    expect(screen.queryByLabelText("Message")).toBeNull();
  });
});

// @vitest-environment jsdom
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Drafts } from "./Drafts";
import { RequestEdits, RequestNotes } from "./RequestNotes";
import { RequestResearch } from "./RequestPlan";
import type { Project, Task } from "./api";

const project: Project = {
  id: "p1",
  title: "Service",
  status: "active",
  prefix: "SE",
  brief: { version: 1, goal: "Faster", criteria: [] },
};
const task: Task = {
  id: "t1",
  ref: "SE-4",
  project_id: "p1",
  objective: "Cache the lookups",
  criteria: ["Reworded"],
  status: "reviewing",
  stage: "reviewing",
  round: 1,
  revisions: [{ n: 1, brief_version: 1, files: [] }],
  verdicts: [
    {
      revision: 1,
      role: "Rune",
      brief_version: 1,
      outcome: "pass",
      summary: "Fine as it is.",
      next: "revise",
      note: "I want the closing warmer",
    },
  ],
  notes: [
    { id: "n1", by: "Rune", kind: "reviewer", text: "Dates are UTC.", at: "" },
    { id: "n2", by: "owner", kind: "owner", text: "Thanks.", at: "" },
  ],
  edits: [
    {
      id: "e1",
      by: "Rhea",
      kind: "researcher",
      before: { objective: "Cache", criteria: ["Owner's words"] },
      after: { objective: "Cache the lookups", criteria: ["Reworded"] },
      at: "",
    },
  ],
  research: [
    {
      id: "r1",
      from: "Rune",
      round: 1,
      revision: 1,
      question: "Does v2 cover it?",
      researcher: "Rhea",
      answered_at: "2026-09-28T10:00:00Z",
    },
  ],
};

function stubFetch(calls: { path: string; options?: RequestInit }[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, options?: RequestInit) => {
      calls.push({ path, options });
      return { ok: true, status: 200, json: async () => [] };
    }),
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("a request's notes and changes", () => {
  it("shows the notes and adds the owner's", async () => {
    const calls: { path: string; options?: RequestInit }[] = [];
    stubFetch(calls);
    const refresh = vi.fn(async () => {});
    render(<RequestNotes task={task} closed={false} refresh={refresh} />);
    expect(screen.getByText("Dates are UTC.")).toBeTruthy();
    expect(screen.getByText("You")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Note"), {
      target: { value: "Keep the old keys" },
    });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Add note" }));
    });
    expect(calls[0].path).toBe("/api/projects/p1/tasks/t1/notes");
    expect(JSON.parse(String(calls[0].options?.body))).toEqual({
      text: "Keep the old keys",
    });
    expect(refresh).toHaveBeenCalled();
  });

  it("still takes the owner's note once the request has finished", async () => {
    const calls: { path: string; options?: RequestInit }[] = [];
    stubFetch(calls);
    render(
      <RequestNotes
        task={{ ...task, status: "landed", notes: [] }}
        closed
        refresh={async () => {}}
      />,
    );
    expect(screen.getByText(/stays with the finished request/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Note"), {
      target: { value: "Worth revisiting in spring" },
    });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Add note" }));
    });
    expect(calls[0].path).toBe("/api/projects/p1/tasks/t1/notes");
  });

  it("lists each change to what was asked, and undoes one", async () => {
    const calls: { path: string; options?: RequestInit }[] = [];
    stubFetch(calls);
    render(
      <RequestEdits task={task} closed={false} refresh={async () => {}} />,
    );
    expect(screen.getByText("Rhea changed it")).toBeTruthy();
    expect(screen.getByText("Added: Reworded")).toBeTruthy();
    expect(screen.getByText("Owner's words")).toBeTruthy();
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: "Undo Rhea's change" }),
      );
    });
    expect(calls[0].path).toBe("/api/projects/p1/tasks/t1/edits/e1/undo");
    expect(calls[0].options?.method).toBe("POST");
  });

  it("offers no undo once the request has finished", () => {
    render(<RequestEdits task={task} closed refresh={async () => {}} />);
    expect(screen.queryByRole("button", { name: /Undo/ })).toBeNull();
  });

  it("shows research a checker asked for, and where it stands", () => {
    render(<RequestResearch research={task.research ?? []} />);
    expect(screen.getByText("Rune asked, checking draft 1")).toBeTruthy();
    expect(screen.getByText("Does v2 cover it?")).toBeTruthy();
    expect(
      screen.getByText("Rhea updated the plan; back with Rune"),
    ).toBeTruthy();
  });

  it("shows a checker's recommendation beside its verdict", () => {
    stubFetch([]);
    render(
      <Drafts project={project} task={task} members={[]} collapsed={false} />,
    );
    expect(
      screen.getByText(
        "Recommends another revision: I want the closing warmer",
      ),
    ).toBeTruthy();
  });
});

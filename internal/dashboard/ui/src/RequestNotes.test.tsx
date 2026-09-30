// @vitest-environment jsdom
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Drafts } from "./Drafts";
import { RequestEdits, RequestNotes } from "./RequestNotes";
import { RequestDesign, RequestResearch } from "./RequestPlan";
import type { Project, Task } from "./api";
import { recordFetch, reply } from "./testFetch";
import { atBottom, layOutScrolling } from "./testScroll";

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

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("a request's notes and changes", () => {
  it("shows the notes and adds the owner's", async () => {
    const { calls } = recordFetch(() => reply([]));
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
    const { calls } = recordFetch(() => reply([]));
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
    const { calls } = recordFetch(() => reply([]));
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
    recordFetch(() => reply([]));
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

describe("lists that grow each round", () => {
  let undo = () => {};
  beforeEach(() => {
    undo = layOutScrolling();
  });
  afterEach(() => undo());
  const many = <T,>(n: number, make: (i: number) => T) =>
    Array.from({ length: n }, (_, i) => make(i + 1));
  /** The request's list in its box, and every entry still in it. */
  function box(section: string, entries: number) {
    const region = screen.getByRole("region", { name: section });
    const bounded = region.querySelector(".bounded") as HTMLElement;
    expect(bounded, `${section} is in a box`).toBeTruthy();
    expect(bounded.tabIndex).toBe(0);
    expect(bounded.children).toHaveLength(entries);
    return bounded;
  }

  it("keeps many notes in a box of their own, open at the newest, with the form below it", () => {
    const notes = many(30, (i) => ({
      id: `n${i}`,
      by: "Rune",
      kind: "reviewer",
      text: `Note ${i}`,
      at: "",
    }));
    render(
      <RequestNotes
        task={{ ...task, notes }}
        closed={false}
        refresh={vi.fn()}
      />,
    );
    const list = box("Notes", 30);
    expect(list.tagName).toBe("OL");
    for (const n of notes)
      expect(list.contains(screen.getByText(n.text))).toBe(true);
    expect(atBottom(list)).toBe(true);
    expect(list.contains(screen.getByLabelText("Note"))).toBe(false);
    expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual([
      "Add note",
    ]);
  });

  it("keeps many changes to what was asked in a box, open at the newest", () => {
    const edits = many(20, (i) => ({
      id: `e${i}`,
      by: "Rhea",
      kind: "researcher",
      before: { objective: `Title ${i - 1}`, criteria: [] },
      after: { objective: `Title ${i}`, criteria: [] },
      at: "",
    }));
    render(
      <RequestEdits
        task={{ ...task, edits }}
        closed={false}
        refresh={async () => {}}
      />,
    );
    const list = box("Changes to what was asked", 20);
    expect(list.scrollTop).toBe(0);
    expect(list.firstElementChild?.textContent).toContain("Title 20");
    expect(list.lastElementChild?.textContent).toContain("Title 1");
    expect(
      screen.getAllByRole("button", { name: "Undo Rhea's change" }),
    ).toHaveLength(20);
  });

  it("keeps many drafts and their verdicts in a box, the newest open at its top", () => {
    recordFetch(() => reply({ files: [] }));
    const revisions = many(25, (n) => ({ n, brief_version: 1, files: [] }));
    const verdicts = many(25, (n) => ({
      revision: n,
      role: "Rune",
      brief_version: 1,
      outcome: "revise",
      summary: `Verdict on ${n}`,
    }));
    render(
      <Drafts
        project={project}
        task={{ ...task, revisions, verdicts }}
        members={[]}
        collapsed={false}
      />,
    );
    const list = box("Drafts", 25);
    expect(list.scrollTop).toBe(0);
    const first = list.firstElementChild as HTMLDetailsElement;
    expect(first.open).toBe(true);
    expect(first.textContent).toContain("Draft 25");
    for (const v of verdicts)
      expect(list.contains(screen.getByText(v.summary))).toBe(true);
  });

  it("keeps the research and design asked for in boxes, open at the newest", () => {
    const research = many(15, (i) => ({
      id: `r${i}`,
      from: "Rune",
      round: i,
      revision: i,
      question: `Question ${i}`,
    }));
    render(<RequestResearch research={research} />);
    const asked = box("Research asked for", 15);
    expect(atBottom(asked)).toBe(true);
    expect(asked.lastElementChild?.textContent).toContain("Question 15");
    const design = many(15, (i) => ({
      id: `d${i}`,
      from: "Rhea",
      step: "researching",
      round: i,
      question: `Look ${i}`,
    }));
    render(<RequestDesign task={{ ...task, design }} />);
    const looks = box("Design input", 15);
    expect(atBottom(looks)).toBe(true);
    for (const d of design)
      expect(looks.contains(screen.getByText(d.question))).toBe(true);
  });
});

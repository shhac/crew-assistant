// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  render,
  screen,
  fireEvent,
  waitFor,
} from "@testing-library/react";
import { BoardCard } from "./BoardCard";
import { TodoQueue } from "./TodoQueue";
import { RequestRelations } from "./RequestRelations";
import { RequestPlan } from "./RequestPlan";
import type { Project, Task } from "./api";

const project: Project = {
  id: "app",
  title: "App",
  status: "active",
  brief: { version: 1, goal: "Work", criteria: [], updated_at: "" },
};
const task: Task = {
  id: "use",
  project_id: "app",
  objective: "Use library",
  criteria: [],
  status: "queued",
  stage: "todo",
  round: 0,
  revisions: [],
  verdicts: [],
  created_at: "",
  depends_on: ["tag"],
  waiting_on: [
    {
      task: "tag",
      ref: "LIB-1",
      project_id: "lib",
      project: "Library",
      objective: "Tag library",
    },
  ],
};
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

for (const surface of ["queue", "card"] as const) {
  it(`links other-project waits on the ${surface}`, () => {
    render(
      surface === "queue" ? (
        <TodoQueue tasks={[task]} project={project} refresh={async () => {}} />
      ) : (
        <BoardCard task={task} />
      ),
    );
    expect(
      screen
        .getByRole("link", { name: "LIB-1 “Tag library”" })
        .getAttribute("href"),
    ).toBe("#/projects/lib/requests/tag");
    expect(screen.getByText(/in Library/)).toBeTruthy();
  });
  it(`links same-project waits without another project name on the ${surface}`, () => {
    const same = {
      ...task,
      waiting_on: [
        { ...task.waiting_on![0], project_id: "app", project: "App" },
      ],
    };
    render(
      surface === "queue" ? (
        <TodoQueue tasks={[same]} project={project} refresh={async () => {}} />
      ) : (
        <BoardCard task={same} />
      ),
    );
    expect(
      screen
        .getByRole("link", { name: "LIB-1 “Tag library”" })
        .getAttribute("href"),
    ).toBe("#/projects/app/requests/tag");
    expect(screen.queryByText(/in App/)).toBeNull();
  });
}

it("resolves a cross-project relation from the wait pointer and lets the owner remove it", async () => {
  const fetch = vi.fn(async () => ({ ok: true, json: async () => task }));
  vi.stubGlobal("fetch", fetch);
  const refresh = vi.fn(async () => {});
  render(
    <RequestRelations
      projects={[project, { ...project, id: "lib", title: "Library" }]}
      project={project}
      task={task}
      tasks={[task]}
      members={[]}
      refresh={refresh}
    />,
  );
  expect(
    screen.getByRole("link", { name: "Tag library" }).getAttribute("href"),
  ).toBe("#/projects/lib/requests/tag");
  expect(screen.getByText("in Library")).toBeTruthy();
  expect(screen.queryByText("A request no longer here")).toBeNull();
  fireEvent.click(
    screen.getByRole("button", {
      name: "Remove “Tag library” from Depends on",
    }),
  );
  await waitFor(() => expect(refresh).toHaveBeenCalledOnce());
  expect(fetch).toHaveBeenCalledWith(
    "/api/projects/app/tasks/use/links/tag",
    expect.objectContaining({ method: "DELETE" }),
  );
});

it("resolves finished dependencies from all tasks", () => {
  const other = {
    ...task,
    id: "tag",
    project_id: "lib",
    objective: "Tag library",
    status: "landed" as const,
  };
  render(
    <RequestRelations
      projects={[project, { ...project, id: "lib", title: "Library" }]}
      project={project}
      task={{ ...task, waiting_on: [] }}
      tasks={[task, other]}
      members={[]}
      refresh={async () => {}}
    />,
  );
  expect(
    screen.getByRole("link", { name: "Tag library" }).getAttribute("href"),
  ).toBe("#/projects/lib/requests/tag");
  expect(screen.getByText("in Library")).toBeTruthy();
});

for (const pending of [true, false]) {
  it(`shows prerequisite guidance with answer pending=${pending} on every surface`, () => {
    const held: Task = {
      ...task,
      waiting_on: [],
      depends_on: [],
      blockers: [
        {
          id: "b",
          kind: "prerequisite",
          description: "lib tagged",
          by: "role:researcher",
          answer_pending: pending,
          at: "",
        },
      ],
    };
    const words = pending
      ? "Waits for your answer: is lib tagged ready?"
      : "Waits for you to clear “lib tagged” on the task page";
    const view = render(<BoardCard task={held} />);
    expect(screen.getByText(words)).toBeTruthy();
    view.unmount();
    const queue = render(
      <TodoQueue tasks={[held]} project={project} refresh={async () => {}} />,
    );
    expect(screen.getByText(words)).toBeTruthy();
    queue.unmount();
    render(
      <RequestRelations
        projects={[project, { ...project, id: "lib", title: "Library" }]}
        project={project}
        task={held}
        tasks={[held]}
        members={[]}
        refresh={async () => {}}
      />,
    );
    expect(screen.getByText(words)).toBeTruthy();
  });
}

it("shows confirmed and dropped prerequisites in the next plan", () => {
  render(
    <RequestPlan
      plan={{
        summary: "Go",
        exists: ["Existing library"],
        role: "Researcher",
        at: "",
        prerequisites: [
          { what: "Library tagged", blocker: "one", outcome: "confirmed" },
          { what: "Owner check", blocker: "two", outcome: "dropped" },
        ],
      }}
    />,
  );
  expect(screen.getByText("Library tagged (confirmed)")).toBeTruthy();
  expect(screen.getByText("Owner check (dropped)")).toBeTruthy();
  expect(
    Array.from(document.querySelectorAll(".plan-part > .label")).map(
      (e) => e.textContent,
    ),
  ).toEqual(["Prerequisites", "What exists"]);
});

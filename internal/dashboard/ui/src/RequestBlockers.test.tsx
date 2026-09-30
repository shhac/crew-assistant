// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { RequestRelations } from "./RequestRelations";
import { TodoQueue } from "./TodoQueue";
import { BoardCard } from "./BoardCard";
import { waitingWords } from "./stages";
import type { Project, Task } from "./api";

const project: Project = {
  id: "p",
  title: "Service",
  status: "active",
  brief: { version: 1, goal: "Work", criteria: [], updated_at: "2026-09-30" },
  playbook: {
    template: "code",
    medium: "git",
    roles: [],
    max_rounds: 3,
    deliver: "owner",
  },
};
const task: Task = {
  id: "t",
  project_id: "p",
  objective: "Use the change",
  criteria: [],
  status: "queued",
  stage: "todo",
  round: 1,
  revisions: [],
  verdicts: [],
  created_at: "2026-09-30",
  blockers: [
    {
      id: "b",
      kind: "manual",
      description: "the build is ready",
      by: "owner",
      at: "2026-09-30",
      check: "No recorded commit",
    },
  ],
};
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function mockFetch() {
  const fetch = vi.fn(async () => ({ ok: true, json: async () => task }));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
it("shows ownership, check reason and clearing on the request", async () => {
  const fetch = mockFetch();
  const refresh = vi.fn(async () => {});
  render(
    <RequestRelations
      project={project}
      task={task}
      tasks={[task]}
      members={[]}
      refresh={refresh}
    />,
  );
  expect(screen.getByText("Set by you")).toBeTruthy();
  expect(screen.getByText("No recorded commit")).toBeTruthy();
  fireEvent.click(
    screen.getByRole("button", { name: "Clear “the build is ready”" }),
  );
  await waitFor(() => expect(refresh).toHaveBeenCalled());
  expect(fetch).toHaveBeenCalledWith(
    "/api/projects/p/tasks/t/blockers/b",
    expect.objectContaining({ method: "DELETE" }),
  );
});
it("adds manual and daemon conditions with the chosen gate", async () => {
  const fetch = mockFetch();
  const refresh = vi.fn(async () => {});
  const other = {
    ...task,
    id: "other",
    ref: "DEMO-2",
    objective: "Change",
    blockers: [],
  };
  render(
    <RequestRelations
      project={project}
      task={{ ...task, blockers: [] }}
      tasks={[task, other]}
      members={[]}
      refresh={refresh}
    />,
  );
  fireEvent.change(screen.getByLabelText("Condition description"), {
    target: { value: "the service is ready" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Add condition" }));
  await waitFor(() => expect(refresh).toHaveBeenCalledTimes(1));
  expect(fetch).toHaveBeenLastCalledWith(
    "/api/projects/p/tasks/t/blockers",
    expect.objectContaining({
      method: "POST",
      body: JSON.stringify({
        kind: "manual",
        description: "the service is ready",
        task: "",
        landing_only: false,
      }),
    }),
  );
  fireEvent.change(screen.getByLabelText("Condition kind"), {
    target: { value: "daemon_includes" },
  });
  fireEvent.change(screen.getByLabelText("Included request"), {
    target: { value: "other" },
  });
  fireEvent.change(screen.getByLabelText("Hold until cleared"), {
    target: { value: "landing" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Add condition" }));
  await waitFor(() => expect(refresh).toHaveBeenCalledTimes(2));
  expect(fetch).toHaveBeenLastCalledWith(
    "/api/projects/p/tasks/t/blockers",
    expect.objectContaining({
      body: JSON.stringify({
        kind: "daemon_includes",
        description: "",
        task: "other",
        landing_only: true,
      }),
    }),
  );
});
it("shows open conditions on cards and retains muted clearing history", () => {
  const view = render(<BoardCard task={task} />);
  expect(screen.getByText("Held until: the build is ready")).toBeTruthy();
  view.unmount();
  render(
    <RequestRelations
      project={project}
      task={{
        ...task,
        blockers: [
          {
            ...task.blockers![0],
            cleared_at: "2026-09-30",
            cleared_by: "daemon",
          },
        ],
      }}
      tasks={[]}
      members={[]}
      refresh={async () => {}}
    />,
  );
  expect(screen.getByText("Set by you · Cleared by the daemon")).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Clear “the build is ready”" }),
  ).toBeNull();
  expect(
    waitingWords(task, {
      kind: "blocker",
      on: "Held until the build is ready",
    }),
  ).toBe("Held until the build is ready");
});

it("shows the condition in the to-do queue alongside task dependencies", () => {
  render(
    <TodoQueue
      project={project}
      tasks={[{ ...task, waits_for: ["Earlier work"] }]}
      refresh={async () => {}}
    />,
  );
  expect(
    screen.getByText(
      "Waits for “Earlier work”; Held until: the build is ready",
    ),
  ).toBeTruthy();
});

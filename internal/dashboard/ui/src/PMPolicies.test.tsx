// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PMPolicies } from "./StaffingSettings";
import { RequestEdits } from "./RequestNotes";
import { recordFetch } from "./testFetch";
import type { Task } from "./api";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("shows five independent default-On policies and sends revision-fenced owner choices", async () => {
  const { calls } = recordFetch();
  const refresh = vi.fn(async () => {});
  const props = { scope: "team" as const, id: "t", refresh, revision: 0 };
  const { rerender } = render(<PMPolicies {...props} />);
  const fields = screen.getAllByRole("combobox") as HTMLSelectElement[];
  expect(fields).toHaveLength(5);
  for (const field of fields) expect(field.value).toBe("default");
  fireEvent.change(screen.getByLabelText("Close queued duplicates"), {
    target: { value: "off" },
  });
  await waitFor(() => expect(refresh).toHaveBeenCalledTimes(1));
  expect(calls[0].path).toBe("/api/teams/t/pm-policies/close_duplicate");
  expect(JSON.parse(String(calls[0].options?.body))).toEqual({
    on: false,
    revision: 0,
  });
  rerender(
    <PMPolicies {...props} revision={1} choices={{ close_duplicate: false }} />,
  );
  expect(
    (screen.getByLabelText("Close queued duplicates") as HTMLSelectElement)
      .value,
  ).toBe("off");
  expect(
    (screen.getByLabelText("Split secondary requirements") as HTMLSelectElement)
      .value,
  ).toBe("default");
  fireEvent.change(screen.getByLabelText("Close queued duplicates"), {
    target: { value: "default" },
  });
  await waitFor(() => expect(refresh).toHaveBeenCalledTimes(2));
  expect(JSON.parse(String(calls[1].options?.body))).toEqual({
    on: null,
    revision: 1,
  });
});

it("uses the project policy route for a teamless project and preserves explicit On", async () => {
  const { calls } = recordFetch();
  const refresh = vi.fn(async () => {});
  render(
    <PMPolicies
      scope="project"
      id="p"
      choices={{ rescope: true }}
      revision={4}
      refresh={refresh}
    />,
  );
  expect(
    (screen.getByLabelText("Split secondary requirements") as HTMLSelectElement)
      .value,
  ).toBe("on");
  fireEvent.change(screen.getByLabelText("Split secondary requirements"), {
    target: { value: "off" },
  });
  await waitFor(() => expect(refresh).toHaveBeenCalled());
  expect(calls[0].path).toBe("/api/projects/p/pm-policies/rescope");
  expect(JSON.parse(String(calls[0].options?.body))).toEqual({
    on: false,
    revision: 4,
  });
});

it("offers owner undo on a stopped duplicate and retains the audit record after undo", async () => {
  const { calls } = recordFetch();
  const refresh = vi.fn(async () => {});
  const task = {
    id: "t",
    project_id: "p",
    status: "stopped",
    pm_actions: [
      {
        id: "a",
        policy: "close_duplicate",
        reason: "Same outcome",
        by: "PM",
        status: "performed",
        at: "2026-11-01T10:00:00Z",
        effects: { duplicate_of: "target" },
      },
    ],
  } as Task;
  const { rerender } = render(
    <RequestEdits task={task} closed refresh={refresh} />,
  );
  expect(screen.getByText("Same outcome")).toBeTruthy();
  fireEvent.click(screen.getByLabelText("Undo PM duplicate closure"));
  await waitFor(() => expect(refresh).toHaveBeenCalled());
  expect(calls[0].path).toBe("/api/projects/p/tasks/t/pm-actions/a/undo");
  rerender(
    <RequestEdits
      task={{
        ...task,
        pm_actions: task.pm_actions?.map((a) => ({ ...a, status: "undone" })),
      }}
      closed={false}
      refresh={refresh}
    />,
  );
  expect(screen.queryByLabelText("Undo PM duplicate closure")).toBeNull();
  expect(screen.getByText("Same outcome")).toBeTruthy();
});

it("names duplicate closures by readable reference, including after undo or target removal", () => {
  const source = {
    id: "source-internal-id",
    project_id: "p",
    status: "stopped",
    pm_actions: [
      {
        id: "a",
        policy: "close_duplicate",
        by: "PM",
        reason: "Same purpose",
        status: "performed",
        at: "2026-11-01T12:00:00Z",
        effects: {
          duplicate_of: "target-internal-id",
          duplicate_label: "SE-2",
        },
      },
    ],
  } as Task;
  const target = {
    id: "target-internal-id",
    ref: "SE-9",
    objective: "Existing request",
  } as Task;
  const { container, rerender } = render(
    <RequestEdits
      task={source}
      tasks={[target]}
      closed
      refresh={async () => {}}
    />,
  );
  expect(screen.getByText("Closed as a duplicate of SE-9")).toBeTruthy();
  expect(container.textContent).not.toContain("target-internal-id");
  expect(container.textContent).not.toContain("close_duplicate");
  rerender(
    <RequestEdits
      task={{
        ...source,
        status: "queued",
        pm_actions: source.pm_actions?.map((a) => ({ ...a, status: "undone" })),
      }}
      closed={false}
      refresh={async () => {}}
    />,
  );
  expect(screen.getByText("Closed as a duplicate of SE-2")).toBeTruthy();
  expect(screen.queryByLabelText("Undo PM duplicate closure")).toBeNull();
  expect(container.textContent).not.toContain("target-internal-id");
});

it("uses current linked task references for earlier action records without a saved label", () => {
  const source = {
    id: "s",
    project_id: "p",
    status: "stopped",
    pm_actions: [
      {
        id: "a",
        policy: "close_duplicate",
        by: "PM",
        reason: "Same purpose",
        status: "performed",
        at: "",
        effects: { duplicate_of: "canonical-target" },
      },
    ],
  } as Task;
  const target = {
    id: "canonical-target",
    ref: "SE-3",
    objective: "Existing request",
  } as Task;
  const { container } = render(
    <RequestEdits
      task={source}
      tasks={[target]}
      closed
      refresh={async () => {}}
    />,
  );
  expect(screen.getByText("Closed as a duplicate of SE-3")).toBeTruthy();
  expect(container.textContent).not.toContain("canonical-target");
  expect(container.textContent).not.toContain("close_duplicate");
});

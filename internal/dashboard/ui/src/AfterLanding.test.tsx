// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  InboxAfterLanding,
  ProjectChecks,
  RequestChecks,
} from "./AfterLanding";
import {
  markOwnerCheck,
  clearOwnerChecks,
  type Task,
  type OutstandingCheck,
} from "./api";

vi.mock("./api", async (original) => ({
  ...(await original<typeof import("./api")>()),
  markOwnerCheck: vi.fn(),
  clearOwnerChecks: vi.fn(),
}));
beforeEach(() => {
  vi.mocked(markOwnerCheck).mockResolvedValue({} as Task);
  vi.mocked(clearOwnerChecks).mockResolvedValue({ cleared: 1 });
  HTMLDialogElement.prototype.showModal = function () {
    this.setAttribute("open", "");
  };
  HTMLDialogElement.prototype.close = function () {
    this.removeAttribute("open");
  };
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
const task = {
  id: "t",
  project_id: "p",
  status: "landed",
  checklist: [{ step: "Live check", status: "outstanding" }],
} as Task;
const check = {
  project_id: "p",
  project: "Notes",
  task_id: "t",
  ref: "CA-1",
  objective: "Build",
  step: "Live check",
  landed_at: "2020-01-01T12:00:00Z",
} as OutstandingCheck;

it("marks done and refreshes, and shows explicit neutral outstanding status", async () => {
  const refresh = vi.fn().mockResolvedValue(undefined);
  render(<RequestChecks task={task} refresh={refresh} />);
  expect(screen.getByText("Outstanding").className).not.toContain("needs");
  fireEvent.click(screen.getByRole("button", { name: "Done: Live check" }));
  await waitFor(() => expect(refresh).toHaveBeenCalledTimes(1));
  expect(markOwnerCheck).toHaveBeenCalledWith(
    "p",
    "t",
    "Live check",
    "done",
    "",
  );
  expect(screen.getByRole("status").textContent).toBe("Live check: Done");
  expect(screen.getAllByRole("status")).toHaveLength(1);
});
it("focuses the skip field, requires a note, retains it on failure and cancels with Escape", async () => {
  vi.mocked(markOwnerCheck).mockRejectedValue(new Error("Try again"));
  render(<RequestChecks task={task} refresh={async () => {}} />);
  fireEvent.click(screen.getByRole("button", { name: "Skip: Live check" }));
  const field = screen.getByLabelText(
    "Why skip this check?",
  ) as HTMLTextAreaElement;
  expect(document.activeElement).toBe(field);
  expect(
    screen.getByText("A short note is required to skip this check."),
  ).toBeTruthy();
  expect(field.required).toBe(true);
  fireEvent.submit(field.closest("form")!);
  expect(markOwnerCheck).not.toHaveBeenCalled();
  fireEvent.change(field, { target: { value: "Not needed" } });
  fireEvent.click(screen.getByRole("button", { name: "Skip check" }));
  await screen.findByRole("alert");
  expect(field.value).toBe("Not needed");
  expect(markOwnerCheck).toHaveBeenCalledWith(
    "p",
    "t",
    "Live check",
    "skipped",
    "Not needed",
  );
  fireEvent.keyDown(field, { key: "Escape" });
  expect(screen.queryByLabelText("Why skip this check?")).toBeNull();
  expect(document.activeElement).toBe(
    screen.getByRole("button", { name: "Skip: Live check" }),
  );
});
it("shows marks and skip notes, undoes and keeps attribution under a disclosure", async () => {
  const mark = {
    step: "Live check",
    status: "skipped" as const,
    by: "assistant",
    at: "2020-01-01T12:00:00Z",
    note: "Not needed",
  };
  const refresh = vi.fn().mockResolvedValue(undefined);
  const view = render(
    <RequestChecks
      task={{
        ...task,
        checklist: [{ step: mark.step, status: "skipped", mark }],
      }}
      refresh={refresh}
    />,
  );
  expect(screen.getByText("Skipped")).toBeTruthy();
  expect(screen.getByText(/Skipped by the assistant/).textContent).toContain(
    "Not needed",
  );
  fireEvent.click(screen.getByRole("button", { name: "Undo: Live check" }));
  await waitFor(() => expect(refresh).toHaveBeenCalled());
  expect(markOwnerCheck).toHaveBeenCalledWith(
    "p",
    "t",
    "Live check",
    "outstanding",
    "",
  );
  view.rerender(
    <RequestChecks
      task={{
        ...task,
        checklist: [
          {
            step: mark.step,
            status: "outstanding",
            history: [mark],
            mark: { ...mark, status: "outstanding", by: "owner", note: "" },
          },
        ],
      }}
      refresh={refresh}
    />,
  );
  const details = screen.getByText("Previous marks").closest("details")!;
  expect(details.open).toBe(false);
  expect(within(details).getByText(/Skipped by the assistant/)).toBeTruthy();
  expect(within(details).getByText(/Outstanding by you/)).toBeTruthy();
});
it("only shows checks on landed or delivered requests, with green only for done", () => {
  const view = render(
    <RequestChecks
      task={{ ...task, status: "stopped" }}
      refresh={async () => {}}
    />,
  );
  expect(screen.queryByText("After landing")).toBeNull();
  view.rerender(
    <RequestChecks
      task={{
        ...task,
        status: "delivered",
        checklist: [{ step: "Live check", status: "done" }],
      }}
      refresh={async () => {}}
    />,
  );
  expect(screen.getByText("Done").className).toContain("tone-done");
});
it("links the ordered backlog with metadata and follows the empty rule", () => {
  const view = render(
    <InboxAfterLanding
      checks={[check, { ...check, task_id: "t2", step: "Tag release" }]}
    />,
  );
  const links = screen.getAllByRole("link");
  expect(screen.getByText("2 checks outstanding")).toBeTruthy();
  expect(links[0].textContent).toContain("Live check");
  expect(links[0].textContent).toContain("Notes");
  expect(links[0].textContent).toContain("Landed");
  expect(links[0].textContent).not.toContain(check.objective);
  expect(links[0].getAttribute("aria-label")).toContain(check.objective);
  expect(links[0].getAttribute("href")).toContain("t");
  expect(links[1].textContent).toContain("Tag release");
  view.rerender(<InboxAfterLanding checks={[]} />);
  expect(screen.queryByText("After landing")).toBeNull();
  view.rerender(<InboxAfterLanding checks={[]} empty />);
  expect(screen.getByText("No after-landing checks outstanding.")).toBeTruthy();
});

it("names each row action by its step", () => {
  render(
    <RequestChecks
      task={{
        ...task,
        checklist: [
          { step: "Live check", status: "outstanding" },
          { step: "Tag release", status: "outstanding" },
          { step: "Redirect", status: "done" },
        ],
      }}
      refresh={async () => {}}
    />,
  );
  for (const step of ["Live check", "Tag release"]) {
    expect(screen.getByRole("button", { name: `Done: ${step}` })).toBeTruthy();
    expect(screen.getByRole("button", { name: `Skip: ${step}` })).toBeTruthy();
  }
  expect(screen.getByRole("button", { name: "Undo: Redirect" })).toBeTruthy();
});

it("keeps checks visible while a previously delivered task lands again", () => {
  const view = render(
    <RequestChecks
      task={{ ...task, status: "landing", landed_at: check.landed_at }}
      refresh={async () => {}}
    />,
  );
  expect(screen.getByText("Outstanding")).toBeTruthy();
  for (const status of ["reviewing", "waiting", "awaiting"] as const) {
    view.rerender(
      <RequestChecks
        task={{ ...task, status, landed_at: check.landed_at }}
        refresh={async () => {}}
      />,
    );
    expect(screen.getByText("Outstanding")).toBeTruthy();
  }
  view.rerender(
    <RequestChecks
      task={{ ...task, status: "landing" }}
      refresh={async () => {}}
    />,
  );
  expect(screen.queryByText("After landing")).toBeNull();
});

it("keeps the project header quiet when no checks are outstanding", () => {
  render(<ProjectChecks project="p" checks={[]} refresh={async () => {}} />);
  expect(screen.queryByText("0 checks outstanding")).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Clear outstanding checks" }),
  ).toBeNull();
});

it.each([0, 1, 3])(
  "reports the actual bulk result %i instead of the dialog's stale count",
  async (cleared) => {
    vi.mocked(clearOwnerChecks).mockResolvedValue({ cleared });
    const refresh = vi.fn().mockResolvedValue(undefined);
    render(<ProjectChecks project="p" checks={[check]} refresh={refresh} />);
    fireEvent.click(
      screen.getByRole("button", { name: "Clear outstanding checks" }),
    );
    expect(screen.getByText("Clear 1 outstanding check?")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Why are you clearing them?"), {
      target: { value: "Old checks" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Skip 1 check" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByRole("status").textContent).toBe(
      `Skipped ${cleared} ${cleared === 1 ? "check" : "checks"}.`,
    );
    expect(refresh).toHaveBeenCalledTimes(1);
  },
);
it("clears the project's outstanding checks through a required-note dialog and refreshes", async () => {
  const refresh = vi.fn().mockResolvedValue(undefined);
  render(<ProjectChecks project="p" checks={[check]} refresh={refresh} />);
  expect(screen.getByText("1 check outstanding")).toBeTruthy();
  fireEvent.click(
    screen.getByRole("button", { name: "Clear outstanding checks" }),
  );
  const dialog = screen.getByRole("dialog");
  expect(within(dialog).getByText("Clear 1 outstanding check?")).toBeTruthy();
  expect(
    within(dialog).getByText(
      "Marks these checks skipped. Completed checks stay done.",
    ),
  ).toBeTruthy();
  const note = screen.getByLabelText(
    "Why are you clearing them?",
  ) as HTMLTextAreaElement;
  expect(note.required).toBe(true);
  expect(
    screen.getByText("A short note is required to skip these checks."),
  ).toBeTruthy();
  expect(document.activeElement).toBe(note);
  fireEvent.submit(note.closest("form")!);
  expect(clearOwnerChecks).not.toHaveBeenCalled();
  fireEvent.change(note, { target: { value: "Old backlog" } });
  fireEvent.click(screen.getByRole("button", { name: "Skip 1 check" }));
  await waitFor(() => expect(refresh).toHaveBeenCalled());
  expect(clearOwnerChecks).toHaveBeenCalledWith("p", "Old backlog");
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

it("keeps the bulk note on error and supports Cancel and Escape", async () => {
  vi.mocked(clearOwnerChecks).mockRejectedValue(new Error("Try again"));
  render(
    <ProjectChecks project="p" checks={[check]} refresh={async () => {}} />,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Clear outstanding checks" }),
  );
  const note = screen.getByLabelText(
    "Why are you clearing them?",
  ) as HTMLTextAreaElement;
  fireEvent.change(note, { target: { value: "Old backlog" } });
  fireEvent.click(screen.getByRole("button", { name: "Skip 1 check" }));
  await screen.findByRole("alert");
  expect(note.value).toBe("Old backlog");
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(screen.queryByRole("dialog")).toBeNull();
  fireEvent.click(
    screen.getByRole("button", { name: "Clear outstanding checks" }),
  );
  fireEvent(
    screen.getByRole("dialog"),
    new Event("cancel", { cancelable: true }),
  );
  expect(screen.queryByRole("dialog")).toBeNull();
});

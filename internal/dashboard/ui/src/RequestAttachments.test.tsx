// @vitest-environment jsdom
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ATTACHMENT_LIMIT_BYTES, attachmentError } from "./attachments";
import { RequestAttachments } from "./RequestAttachments";
import { RequestNotes } from "./RequestNotes";
import { RequestDesign } from "./RequestPlan";
import type { Task } from "./api";
import { recordFetch, reply } from "./testFetch";

const task: Task = {
  id: "t1",
  ref: "SE-4",
  project_id: "p1",
  objective: "Settings page",
  criteria: [],
  status: "writing",
  stage: "implementing",
  round: 1,
  revisions: [],
  verdicts: [],
  notes: [{ id: "n1", by: "owner", kind: "owner", text: "Like this", at: "" }],
  design: [
    {
      id: "d1",
      n: 1,
      marked: true,
      from: "Ada",
      step: "writing",
      round: 1,
      question: "Tabs or a sidebar?",
      designer: "Misha",
      input: "Tabs across the top.",
    },
    {
      id: "d2",
      n: 2,
      marked: true,
      from: "Ada",
      step: "writing",
      round: 1,
      question: "Still tabs?",
      designer: "Misha",
      input: "A sidebar after all.",
    },
    {
      id: "d3",
      n: 3,
      from: "Ada",
      step: "writing",
      round: 1,
      question: "Which blue?",
      designer: "Misha",
      input: "The brand blue.",
    },
  ],
  current_design: "d2",
  attachments: [
    {
      id: "a1",
      name: "layout.png",
      type: "image/png",
      size: 2048,
      by: "owner",
      kind: "owner",
      at: "",
      note: "n1",
    },
    {
      id: "a2",
      name: "copy.md",
      type: "text/markdown",
      size: 12,
      by: "owner",
      kind: "owner",
      at: "",
      note: "n1",
    },
    {
      id: "a3",
      name: "sidebar.svg",
      type: "image/svg+xml",
      size: 300,
      by: "Misha",
      kind: "designer",
      at: "",
      design: "d2",
    },
    {
      id: "a4",
      name: "tabs.svg",
      type: "image/svg+xml",
      size: 300,
      by: "Misha",
      kind: "designer",
      at: "",
      design: "d1",
    },
  ],
};

const file = (name: string, size = 10, type = "") =>
  new File([new Uint8Array(size)], name, { type });

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("a request's attachments", () => {
  it("refuses a file with the daemon's reasons before sending it", () => {
    expect(attachmentError(file("run.sh"), 0)).toContain(
      "run.sh can't be attached: only images (PNG, JPEG, GIF, WebP), PDF",
    );
    expect(
      attachmentError(file("big.png", ATTACHMENT_LIMIT_BYTES + 1), 0),
    ).toBe(
      "big.png can't be attached: it is 5,242,881 bytes, and a file can be at most 5,242,880 bytes.",
    );
    expect(attachmentError(file("empty.md", 0), 0)).toContain("it is empty");
    expect(attachmentError(file("eleventh.md"), 10)).toContain(
      "at most 10 files",
    );
    expect(attachmentError(file("Mock.PNG"), 0)).toBe("");
  });

  it("attaches picked, dropped and pasted files to the owner's note, refusing the rest", async () => {
    const { calls } = recordFetch(() => reply({}, 201));
    const refresh = vi.fn(async () => {});
    render(
      <RequestNotes
        task={{ ...task, attachments: [] }}
        closed={false}
        refresh={refresh}
      />,
    );
    fireEvent.change(screen.getByLabelText("Attach files"), {
      target: { files: [file("layout.png"), file("run.sh")] },
    });
    const form = screen.getByLabelText("Note").closest("form")!;
    fireEvent.drop(form, {
      dataTransfer: { files: [file("flow.pdf")], types: ["Files"] },
    });
    fireEvent.paste(screen.getByLabelText("Note"), {
      clipboardData: { files: [file("shot.png")], getData: () => "" },
    });
    const chips = screen.getByRole("list", { name: "Files to attach" });
    expect(within(chips).getByText("layout.png")).toBeTruthy();
    expect(within(chips).getByText("flow.pdf")).toBeTruthy();
    expect(within(chips).getByText("shot.png")).toBeTruthy();
    expect(within(chips).queryByText("run.sh")).toBeNull();
    fireEvent.change(screen.getByLabelText("Attach files"), {
      target: { files: [file("run.sh")] },
    });
    expect(screen.getByRole("alert").textContent).toContain(
      "run.sh can't be attached",
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Remove attachment flow.pdf" }),
    );
    // Files alone make a note.
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Add note" }));
    });
    expect(calls[0].path).toBe("/api/projects/p1/tasks/t1/notes");
    const body = calls[0].options?.body as FormData;
    expect(body).toBeInstanceOf(FormData);
    expect(body.get("text")).toBe("");
    expect(body.getAll("files").map((f) => (f as File).name)).toEqual([
      "layout.png",
      "shot.png",
    ]);
    const headers = calls[0].options?.headers as Record<string, string>;
    expect(headers["Content-Type"]).toBeUndefined();
    expect(refresh).toHaveBeenCalled();
    expect(screen.queryByRole("list", { name: "Files to attach" })).toBeNull();
  });

  it("shows a note's files: pictures as thumbnails, the rest as links", () => {
    recordFetch(() => reply({}, 201));
    render(
      <RequestNotes task={task} closed={false} refresh={async () => {}} />,
    );
    const shown = screen.getByRole("list", { name: "Attached files" });
    const picture = within(shown).getByAltText("layout.png");
    expect(picture.getAttribute("src")).toBe(
      "/api/projects/p1/tasks/t1/attachments/a1",
    );
    expect(
      within(shown).getByRole("link", { name: "copy.md" }).getAttribute("href"),
    ).toBe("/api/projects/p1/tasks/t1/attachments/a2");
    expect(within(shown).queryByAltText("copy.md")).toBeNull();
  });

  it("numbers the designs and marks which is current, superseded or advice", () => {
    render(<RequestDesign task={task} />);
    const first = screen.getByLabelText("Design 1");
    const second = screen.getByLabelText("Design 2");
    const third = screen.getByLabelText("Design 3");
    expect(within(second).getByText("Current design")).toBeTruthy();
    expect(within(second).getByAltText("sidebar.svg")).toBeTruthy();
    expect(within(first).getByText("Superseded")).toBeTruthy();
    expect(first.className).toContain("superseded");
    expect(within(first).getByText(/Not the current design/)).toBeTruthy();
    expect(within(first).getByAltText("tabs.svg")).toBeTruthy();
    expect(within(third).getByText("Advice")).toBeTruthy();
    expect(screen.getAllByText("Current design")).toHaveLength(1);
  });

  it("lists every file in one place, saying what each came with", () => {
    render(<RequestAttachments task={task} />);
    const all = screen.getByRole("region", { name: "Attachments" });
    expect(within(all).getAllByText("With your note")).toHaveLength(2);
    expect(within(all).getByText("Design 2 · Current design")).toBeTruthy();
    expect(within(all).getByText("Design 1 · Superseded")).toBeTruthy();
    expect(
      within(all).getByText("4 of 120 files this request can keep"),
    ).toBeTruthy();
  });

  it("shows no attachments area for a request without files", () => {
    render(<RequestAttachments task={{ ...task, attachments: [] }} />);
    expect(screen.queryByRole("region", { name: "Attachments" })).toBeNull();
  });
});

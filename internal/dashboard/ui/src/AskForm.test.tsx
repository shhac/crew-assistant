// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { AskForm } from "./AskForm";
import { askForTask, type Project, type Task } from "./api";
vi.mock("./api", async (original) => ({
  ...(await original<typeof import("./api")>()),
  askForTask: vi.fn(),
}));
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
const project = {
  id: "app",
  title: "App",
  brief: { goal: "Build" },
  playbook: {},
} as Project;
const library = { ...project, id: "lib", title: "Library" };
const local = {
  id: "local",
  project_id: "app",
  ref: "APP-1",
  objective: "Local",
  status: "queued",
} as Task;
const remote = {
  ...local,
  id: "remote",
  project_id: "lib",
  ref: "LIB-11",
  objective: "Publish",
};
function setup() {
  render(
    <AskForm
      project={project}
      projects={[library, project]}
      tasks={[
        local,
        remote,
        { ...remote, id: "done", ref: "LIB-12", status: "landed" },
        { ...remote, id: "stopped", ref: "LIB-13", status: "stopped" },
      ]}
      refresh={async () => {}}
    />,
  );
  fireEvent.change(screen.getByLabelText("What do you want?"), {
    target: { value: "Use library" },
  });
  fireEvent.click(screen.getByText("+ Say what it waits for"));
}
function choose() {
  fireEvent.change(screen.getByLabelText("What it waits for"), {
    target: { value: "remote" },
  });
}
it("groups unfinished tasks by project, removes choices, and submits canonical ids", async () => {
  vi.mocked(askForTask).mockResolvedValue(remote);
  setup();
  expect(
    screen.getAllByRole("group").map((g) => g.getAttribute("label")),
  ).toEqual(["App", "Library"]);
  expect(screen.getByRole("option", { name: "APP-1 Local" })).toBeTruthy();
  expect(screen.getByRole("option", { name: "LIB-11 Publish" })).toBeTruthy();
  expect(screen.queryByRole("option", { name: /LIB-12|LIB-13/ })).toBeNull();
  choose();
  expect(screen.getByText("in Library")).toBeTruthy();
  fireEvent.click(
    screen.getByRole("button", { name: "Remove “Publish” from waits for" }),
  );
  expect(screen.queryByText("in Library")).toBeNull();
  choose();
  fireEvent.click(screen.getByRole("button", { name: "Ask" }));
  await waitFor(() =>
    expect(askForTask).toHaveBeenCalledWith(
      "app",
      expect.objectContaining({ depends_on: ["remote"] }),
    ),
  );
  await waitFor(() => expect(screen.queryByText("in Library")).toBeNull());
});
it("keeps chosen dependencies when current-state validation refuses submission", async () => {
  vi.mocked(askForTask).mockRejectedValue(new Error("Publish has finished"));
  setup();
  choose();
  fireEvent.click(screen.getByRole("button", { name: "Ask" }));
  await waitFor(() =>
    expect(screen.getByText("Publish has finished")).toBeTruthy(),
  );
  expect(screen.getByText("in Library")).toBeTruthy();
  expect(screen.getByLabelText("What do you want?")).toHaveProperty(
    "value",
    "Use library",
  );
});

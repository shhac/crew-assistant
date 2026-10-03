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
import { AdvancedSettings } from "./AdvancedSettings";
import { type Config, type Project, askForTask } from "./api";

vi.mock("./api", async (original) => ({
  ...(await original<typeof import("./api")>()),
  askForTask: vi.fn().mockResolvedValue({}),
}));
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it("sends owner checks separately from team criteria", async () => {
  const project = {
    id: "p",
    brief: { goal: "Build" },
    playbook: {},
  } as Project;
  render(
    <AskForm
      project={project}
      tasks={[]}
      projects={[project]}
      refresh={async () => {}}
    />,
  );
  fireEvent.change(screen.getByLabelText("What do you want?"), {
    target: { value: "Build" },
  });
  fireEvent.click(screen.getByText("+ Say how you'll judge it"));
  fireEvent.change(
    screen.getByLabelText("How you'll judge it, one point per line"),
    { target: { value: "Tests pass" } },
  );
  fireEvent.click(screen.getByText("+ Say what you'll check after it lands"));
  fireEvent.change(
    screen.getByLabelText(
      "What you'll check after it lands, one point per line",
    ),
    { target: { value: "CI green\nTag release" } },
  );
  fireEvent.click(screen.getByRole("button", { name: /^Ask$/ }));
  await waitFor(() =>
    expect(askForTask).toHaveBeenCalledWith("p", {
      objective: "Build",
      criteria: ["Tests pass"],
      owner_checks: ["CI green", "Tag release"],
    }),
  );
});

it("offers the decision JSONL download in Advanced Settings", () => {
  render(<AdvancedSettings config={{} as Config} onChange={() => {}} />);
  expect(
    screen.getByRole("link", { name: "Download JSONL" }).getAttribute("href"),
  ).toBe("/api/decisions/evaluations.jsonl");
});

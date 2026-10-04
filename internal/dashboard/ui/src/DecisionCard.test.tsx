// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  render,
  screen,
  fireEvent,
  waitFor,
} from "@testing-library/react";
import { DecisionCard } from "./DecisionCard";
import type { Decision, Project, Task } from "./api";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("owner answer hint", () => {
  it("describes the answer field and keeps the submitted wording", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValue(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetch);
    render(
      <DecisionCard decision={decision} task={task} refresh={async () => {}} />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Request changes" }));
    const field = screen.getByRole("textbox");
    const hint = screen.getByText(/Explicit wording such as/);
    expect(field.getAttribute("aria-describedby")).toBe(hint.id);
    const answer = "Add a test; after landing, I will review it visually.";
    fireEvent.change(field, { target: { value: answer } });
    fireEvent.submit(field.closest("form")!);
    await waitFor(() => expect(fetch).toHaveBeenCalled());
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ answer });
  });

  it("does not describe dismissal forms as owner steps", () => {
    render(<DecisionCard decision={decision} refresh={async () => {}} />);
    fireEvent.click(
      screen.getByRole("button", { name: "Close without deciding" }),
    );
    expect(screen.queryByText(/Explicit wording such as/)).toBeNull();
    expect(
      screen.getByRole("textbox").getAttribute("aria-describedby"),
    ).toBeNull();
  });
});

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
  criteria: [],
  status: "waiting",
  stage: "ready",
  round: 1,
  revisions: [],
  verdicts: [],
};
const decision: Decision = {
  id: "d1",
  project_id: "p1",
  task_id: "t1",
  kind: "delivery",
  title: "Approve “Cache the lookups”",
  context: "Ready",
  recommendation: "Approve",
  choices: ["Approve", "Request changes"],
  status: "open",
};

describe("a decision in the inbox", () => {
  it("names the request it holds by its readable ID", () => {
    render(
      <DecisionCard
        decision={decision}
        project={project}
        task={task}
        refresh={async () => {}}
      />,
    );
    expect(screen.getByText("SE-4")).toBeTruthy();
  });
});

describe("splitting an unreachable requirement", () => {
  it("opens prefilled parts and sends the edited split", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValue(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetch);
    const refresh = vi.fn().mockResolvedValue(undefined);
    render(
      <DecisionCard
        decision={{
          ...decision,
          kind: "escalation",
          choices: [
            "Make it an owner step",
            "Split it",
            "Keep it for the team",
          ],
          owner_step: {
            criterion: "README; CI; release",
            step: "Check CI and release",
          },
        }}
        task={task}
        refresh={refresh}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Split it" }));
    expect(fetch).not.toHaveBeenCalled();
    const team = screen.getByLabelText("Stays with the team");
    const owner = screen.getByLabelText("You check after it lands");
    expect((team as HTMLTextAreaElement).value).toBe("README; CI; release");
    expect((owner as HTMLTextAreaElement).value).toBe("Check CI and release");
    fireEvent.change(team, { target: { value: " Check CI and release " } });
    expect(
      (screen.getByRole("button", { name: "Split it" }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);
    fireEvent.submit(team.closest("form")!);
    expect(fetch).not.toHaveBeenCalled();
    fireEvent.change(team, { target: { value: " README updated " } });
    fireEvent.change(owner, { target: { value: " CI green and release " } });
    fireEvent.click(screen.getByRole("button", { name: "Split it" }));
    await waitFor(() => expect(refresh).toHaveBeenCalledOnce());
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({
      choice: "Split it",
      split: { team: "README updated", owner: "CI green and release" },
    });
  });
  it("keeps the existing owner-step choice immediate", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValue(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetch);
    render(
      <DecisionCard
        decision={{ ...decision, choices: ["Make it an owner step"] }}
        task={task}
        refresh={async () => {}}
      />,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Make it an owner step" }),
    );
    await waitFor(() => expect(fetch).toHaveBeenCalledOnce());
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({
      choice: "Make it an owner step",
    });
  });
});

// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { DecisionCard } from "./DecisionCard";
import type { Decision, Project, Task } from "./api";

afterEach(cleanup);

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

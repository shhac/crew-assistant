// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { DecisionHistory } from "./DecisionHistory";
import type { Decision, Project } from "./api";

const projects: Project[] = [
  {
    id: "project-1",
    title: "Garden planner",
    description: "",
    acceptance_criteria: [],
    status: "active",
  },
];

const answered: Decision = {
  id: "decision-answered",
  project_id: "project-1",
  title: "Which review depth?",
  context: "Two reviewers were available for the planting view.",
  recommendation: "Take the focused review.",
  choices: ["Focused review", "Full review"],
  status: "resolved",
  answer: "Focused review",
  disposition: "choice",
  resolved_at: "2026-09-16T16:20:00Z",
};

const dismissed: Decision = {
  id: "decision-dismissed",
  project_id: "project-1",
  title: "Should we add lifecycle controls?",
  context: "The controls question predated the current dashboard.",
  recommendation: "Add owner controls.",
  choices: ["Add", "Skip"],
  status: "dismissed",
  resolution_reason: "Already configured",
  disposition: "dismissed",
  resolved_at: "2026-09-16T16:45:00Z",
};

afterEach(() => {
  cleanup();
});

describe("decision history", () => {
  it("keeps answers and dismissals visually and semantically distinct", () => {
    render(
      <DecisionHistory decisions={[answered, dismissed]} projects={projects} />,
    );
    expect(screen.getByText(/Answer: Focused review/)).toBeTruthy();
    expect(screen.getByText(/Reason: Already configured/)).toBeTruthy();
    const rows = document.querySelectorAll(".history-row");
    expect(rows[0].className).toContain("answered");
    expect(rows[1].className).toContain("dismissed");
  });

  it("links each closed question to its project and records when it closed", () => {
    render(<DecisionHistory decisions={[answered]} projects={projects} />);
    const link = screen.getByRole("link", { name: "Garden planner" });
    expect(link.getAttribute("href")).toBe("#/projects/project-1");
    expect(document.querySelector("time")?.getAttribute("datetime")).toBe(
      "2026-09-16T16:20:00Z",
    );
  });

  it("keeps the original context inspectable and says a dismissal approved nothing", () => {
    render(<DecisionHistory decisions={[dismissed]} projects={projects} />);
    expect(screen.getByText("Original context")).toBeTruthy();
    expect(
      screen.getByText(/The controls question predated the current dashboard./),
    ).toBeTruthy();
    expect(
      screen.getByText(
        /did not approve or restart any work/,
      ),
    ).toBeTruthy();
  });

  it("renders nothing when no decision has been closed", () => {
    const { container } = render(
      <DecisionHistory decisions={[]} projects={projects} />,
    );
    expect(container.innerHTML).toBe("");
  });
});

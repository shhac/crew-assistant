// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { App } from "./App";
import { normalizeState } from "./api";

beforeEach(() => window.history.replaceState(null, "", "/"));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("project context and next outcome", () => {
  it("links decisions, activity and interrupted operations by name and sends the owner's next outcome", async () => {
    const project = {
      id: "project-internal-42",
      title: "Garden planner",
      description: "A personal planning tool",
      status: "active",
      acceptance_criteria: [],
      directories: [],
    };
    const state = normalizeState({
      assistant: { name: "Iris", personality: "" },
      projects: [project],
      decisions: [
        {
          id: "choice-1",
          project_id: project.id,
          title: "Choose a scope",
          context: "Clarify the scope",
          recommendation: "Start small",
          choices: ["Small"],
          status: "pending",
        },
      ],
      activity: [
        {
          id: "activity-1",
          project_id: project.id,
          summary: "Context recorded",
        },
      ],
      pending_operations: [
        {
          id: "operation-1",
          project_id: project.id,
          summary: "Check the interrupted worker",
        },
      ],
    });
    const calls: { path: string; options?: RequestInit }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (path: string, options?: RequestInit) => {
        calls.push({ path, options });
        return {
          ok: true,
          status: 200,
          json: async () => (path === "/api/state" ? state : { workers: [] }),
        };
      }),
    );
    render(<App />);
    await screen.findByRole("button", { name: "Iris overview" });
    const decision = screen
      .getByRole("heading", { name: "Choose a scope" })
      .closest("article")!;
    expect(
      within(decision)
        .getByRole("link", { name: "Garden planner" })
        .getAttribute("href"),
    ).toBe("#/projects/project-internal-42");
    const activity = screen.getByText("Context recorded").closest("li")!;
    fireEvent.click(
      within(activity).getByRole("link", { name: "Garden planner" }),
    );
    const next = await screen.findByRole("textbox", {
      name: "What would you like to do next?",
    });
    expect(window.location.hash).toBe("#/projects/project-internal-42");
    fireEvent.change(next, {
      target: {
        value: "Make the seasonal planting view easier to understand.",
      },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Work on this with me" }),
    );
    await waitFor(() =>
      expect(calls.some((call) => call.path.endsWith("/coordinate"))).toBe(
        true,
      ),
    );
    const request = calls.find((call) => call.path.endsWith("/coordinate"))!;
    expect(request.options?.method).toBe("POST");
    expect(JSON.parse(request.options?.body as string)).toEqual({
      next: "Make the seasonal planting view easier to understand.",
    });
    fireEvent.click(screen.getByRole("button", { name: /^Decisions/ }));
    expect(window.location.hash).toBe("");
    const operations = await screen.findByRole("region", {
      name: "Interrupted operations",
    });
    fireEvent.click(
      within(operations).getByRole("link", { name: "Garden planner" }),
    );
    await screen.findByRole("textbox", {
      name: "What would you like to do next?",
    });
    fireEvent.click(screen.getByRole("button", { name: /All projects/ }));
    expect(window.location.hash).toBe("");
    expect(
      screen.queryByRole("textbox", {
        name: "What would you like to do next?",
      }),
    ).toBeNull();
  });

  // The condition from the owner's review: nothing is waiting on their
  // judgment, the project reads Active, and a worker has stopped.
  it("shows a blocked worker on the overview when no decision is waiting", async () => {
    const project = {
      id: "project-suggestions",
      title: "Agent assistant",
      description: "Coordination dashboard",
      status: "active",
      acceptance_criteria: [],
      directories: [],
    };
    const state = normalizeState({
      assistant: { name: "Iris", personality: "" },
      projects: [project],
      decisions: [],
      work_items: [
        {
          id: "work-suggestions",
          project_id: project.id,
          title: "Suggestions",
          objective: "Offer suggestions",
          acceptance_criteria: "Reviewed",
          status: "blocked",
          status_reason: "Execution is blocked",
          created_at: "2026-09-16T12:00:00Z",
          updated_at: "2026-09-16T16:53:00Z",
          review_revision: "rev-1",
        },
      ],
      agents: [
        {
          id: "agent-suggestions",
          project_id: project.id,
          work_item_id: "work-suggestions",
          name: "Suggestions worker",
          role: "worker",
          status: "blocked",
          summary: "The attempt stopped without a classified provider error.",
          provider_failure_kind: "unknown",
          model_failure_evidence: "untyped_error",
        },
      ],
      attention: [
        {
          project_id: project.id,
          work_item_id: "work-suggestions",
          agent_id: "agent-suggestions",
          agent_name: "Suggestions worker",
          execution: "blocked",
          reason: "The attempt stopped without a classified provider error.",
          next_action: "owner",
          recovery: "held",
          last_progress_at: "2026-09-16T16:53:00Z",
          open_decisions: 0,
          pending_operations: 0,
        },
      ],
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (path: string) => ({
        ok: true,
        status: 200,
        json: async () => (path === "/api/state" ? state : {}),
      })),
    );
    render(<App />);

    const attention = await screen.findByRole("region", {
      name: "Work needing attention",
    });
    expect(screen.queryByText("No decisions waiting on you")).toBeNull();
    expect(attention.textContent).toContain("Suggestions worker");
    expect(attention.textContent).toContain("You act next");

    // The attention summary precedes the project list, so the blocker is the
    // first thing the overview reports rather than something to scroll for.
    const projectsSection = screen.getByText("Projects in motion");
    expect(
      attention.compareDocumentPosition(projectsSection) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();

    // The project still reads Active; health is reported separately.
    fireEvent.click(
      within(attention).getByRole("button", { name: /Agent assistant/ }),
    );
    expect(window.location.hash).toBe("#/projects/project-suggestions");
    expect(
      await screen.findByLabelText("What would you like to do next?"),
    ).toBeTruthy();
  });

  it("puts current work above setup, planning and technical identifiers", async () => {
    const project = {
      id: "project-order",
      title: "Agent assistant",
      description: "Coordination dashboard",
      status: "active",
      acceptance_criteria: ["Reviewed"],
      directories: ["/home/crew-assistant"],
    };
    const state = normalizeState({
      assistant: { name: "Iris", personality: "" },
      projects: [project],
      work_items: [
        {
          id: "work-1",
          project_id: project.id,
          title: "Suggestions",
          objective: "Offer suggestions",
          acceptance_criteria: "Reviewed",
          status: "active",
          created_at: "2026-09-16T12:00:00Z",
          updated_at: "2026-09-16T12:00:00Z",
          review_revision: "rev-1",
        },
      ],
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (path: string) => ({
        ok: true,
        status: 200,
        json: async () => (path === "/api/state" ? state : {}),
      })),
    );
    window.history.replaceState(null, "", "/#/projects/project-order");
    render(<App />);

    const work = await screen.findByLabelText("Project work");
    const reference = screen.getByText("Brief, folders and worker setup");
    const next = screen.getByLabelText("What would you like to do next?");

    // Work first, then the conversational entry point, then everything that is
    // setup or reference material.
    expect(
      work.compareDocumentPosition(next) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(
      next.compareDocumentPosition(reference) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();

    // Setup material is collapsed, and the project id is not in the first view.
    expect((reference.parentElement as HTMLDetailsElement).open).toBe(false);
    expect(screen.queryByText("Technical identifiers")).toBeTruthy();
    expect(
      (screen.getByText("Technical identifiers")
        .parentElement as HTMLDetailsElement).open,
    ).toBe(false);
  });

  it("shows a usage hold beside the work it is holding up", async () => {
    const project = {
      id: "project-hold",
      title: "Release checklist",
      description: "",
      status: "active",
      acceptance_criteria: [],
      directories: [],
    };
    const state = normalizeState({
      assistant: { name: "Iris", personality: "" },
      projects: [project],
      integrations: [
        {
          id: "worker-usage:managed-project-hold",
          project_id: "project-hold",
          name: "Usage for Worker for Release checklist",
          status: "paused",
          detail: "Claude usage is at 94% consumed; new worker work is paused",
        },
        {
          id: "worker-usage:managed-other-project",
          project_id: "other-project",
          name: "Usage for another worker",
          status: "paused",
          detail: "Unrelated hold",
        },
      ],
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (path: string) => ({
        ok: true,
        status: 200,
        json: async () => (path === "/api/state" ? state : {}),
      })),
    );
    window.history.replaceState(null, "", "/#/projects/project-hold");
    render(<App />);
    expect(
      await screen.findByText("New work is held by a usage limit"),
    ).toBeTruthy();
    expect(
      screen.getByText(
        "Claude usage is at 94% consumed; new worker work is paused",
      ),
    ).toBeTruthy();
    // Another project's hold is not this project's problem.
    expect(screen.queryByText("Unrelated hold")).toBeNull();
  });
});

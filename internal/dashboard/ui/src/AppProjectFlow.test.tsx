// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { App } from "./App";
import { normalizeState } from "./api";

beforeEach(() => window.history.replaceState(null, "", "/"));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("project context", () => {
  it("links decisions, activity and interrupted operations to the project by name", async () => {
    const project = {
      id: "project-internal-42",
      title: "Garden planner",
      status: "active",
      brief: { version: 1, goal: "A personal planning tool", criteria: [] },
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
          summary: "Check the interrupted import",
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
    await screen.findByRole("region", { name: "Brief" });
    expect(window.location.hash).toBe("#/projects/project-internal-42");
    expect(
      within(
        screen.getByRole("region", { name: "Decisions for this project" }),
      ).getByRole("heading", { name: "Choose a scope" }),
    ).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /^Decisions/ }));
    expect(window.location.hash).toBe("#/decisions");
    const operations = await screen.findByRole("region", {
      name: "Interrupted operations",
    });
    fireEvent.click(
      within(operations).getByRole("link", { name: "Garden planner" }),
    );
    await screen.findByRole("region", { name: "Brief" });
    fireEvent.click(screen.getByRole("button", { name: /All projects/ }));
    expect(window.location.hash).toBe("#/projects");
    expect(screen.queryByRole("region", { name: "Brief" })).toBeNull();
  });

  it("counts decisions and interrupted operations together on the overview", async () => {
    const state = normalizeState({
      assistant: { name: "Iris", personality: "" },
      projects: [],
      decisions: [
        {
          id: "choice-1",
          title: "Choose a scope",
          context: "",
          recommendation: "",
          choices: [],
          status: "pending",
        },
      ],
      pending_operations: [{ id: "operation-1", summary: "Check the import" }],
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
    expect(
      await screen.findByRole("heading", {
        name: "1 decision and 1 interrupted operation waiting on you",
      }),
    ).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /^Review/ }));
    expect(
      await screen.findByRole("region", { name: "Interrupted operations" }),
    ).toBeTruthy();
  });

  it("says plainly when nothing needs the owner", async () => {
    const state = normalizeState({
      assistant: { name: "Iris", personality: "" },
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
    expect(
      await screen.findByRole("heading", {
        name: "Nothing needs you right now",
      }),
    ).toBeTruthy();
  });

  it("shows the brief and folders, with technical identifiers collapsed", async () => {
    const project = {
      id: "project-order",
      title: "Release checklist",
      status: "active",
      brief: {
        version: 2,
        goal: "Make releases routine",
        criteria: ["Reviewed"],
      },
      directories: ["/home/crew-assistant"],
    };
    const state = normalizeState({
      assistant: { name: "Iris", personality: "" },
      projects: [project],
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

    await screen.findByRole("region", { name: "Brief" });
    expect(screen.getByText("Reviewed")).toBeTruthy();
    expect(screen.getByText(/Version 2/)).toBeTruthy();
    expect(
      screen.getAllByText(/\/home\/crew-assistant/).length,
    ).toBeGreaterThan(0);
    expect(
      (
        screen.getByText("Technical identifiers")
          .parentElement as HTMLDetailsElement
      ).open,
    ).toBe(false);
  });
});

// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { RequestRelations } from "./RequestRelations";
import type { Project, Task } from "./api";

const project = (stack: boolean): Project => ({
  id: "app",
  title: "App",
  status: "active",
  brief: { version: 1, goal: "Work", criteria: [], updated_at: "" },
  playbook: {
    template: "code",
    medium: "git",
    roles: [],
    max_rounds: 3,
    deliver: "",
    land: { pull_requests: true, target: "main", github: "o/r", stack },
  },
});
const request = (
  id: string,
  objective: string,
  extra: Partial<Task> = {},
): Task => ({
  id,
  project_id: "app",
  objective,
  criteria: [],
  status: "queued",
  stage: "todo",
  round: 0,
  revisions: [],
  verdicts: [],
  created_at: "",
  ...extra,
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("stacks a request on another's pull request only where stacking is on", async () => {
  const fetch = vi.fn(async () => ({ ok: true, json: async () => ({}) }));
  vi.stubGlobal("fetch", fetch);
  const refresh = vi.fn(async () => {});
  const schema = request("schema", "Add the schema");
  const api = request("api", "Add the API");
  const { rerender } = render(
    <RequestRelations
      projects={[project(false)]}
      project={project(false)}
      task={api}
      tasks={[schema, api]}
      members={[]}
      refresh={refresh}
    />,
  );
  const kinds = () =>
    within(screen.getByRole("combobox", { name: "Relation" }))
      .getAllByRole("option")
      .map((o) => o.textContent);
  expect(kinds()).not.toContain("Stacked on");
  rerender(
    <RequestRelations
      projects={[project(true)]}
      project={project(true)}
      task={api}
      tasks={[schema, api]}
      members={[]}
      refresh={refresh}
    />,
  );
  expect(kinds()).toContain("Stacked on");
  fireEvent.change(screen.getByRole("combobox", { name: "Relation" }), {
    target: { value: "stacks_on" },
  });
  fireEvent.change(screen.getByRole("combobox", { name: "Other request" }), {
    target: { value: "schema" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Add" }));
  await waitFor(() => expect(refresh).toHaveBeenCalledOnce());
  expect(fetch).toHaveBeenCalledWith(
    "/api/projects/app/tasks/api/links",
    expect.objectContaining({
      method: "POST",
      body: JSON.stringify({ relation: "stacks_on", task: "schema" }),
    }),
  );
});

it("shows what a request is stacked on, and who stacked it", () => {
  const schema = request("schema", "Add the schema");
  const api = request("api", "Add the API", {
    stacks_on: "schema",
    linked_by: { "stacks_on:schema": { by: "pm", at: "" } },
  });
  render(
    <RequestRelations
      projects={[project(true)]}
      project={project(true)}
      task={api}
      tasks={[schema, api]}
      members={[]}
      refresh={async () => {}}
    />,
  );
  const stacked = screen.getByRole("list", { name: "Stacked on" });
  expect(
    within(stacked).getByRole("link", { name: "Add the schema" }),
  ).toBeTruthy();
  expect(within(stacked).getByText("Set by the PM")).toBeTruthy();
});

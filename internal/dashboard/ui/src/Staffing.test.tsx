// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ProjectSetupCard } from "./ProjectSetup";
import { RepositoriesSettings, TeamsSettings } from "./StaffingSettings";
import type { Playbook, Project, Repository, State, Team } from "./api";
import { recordFetch, type FetchCall } from "./testFetch";

let calls: FetchCall[];
beforeEach(() => {
  calls = recordFetch().calls;
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const playbook = {
  template: "code",
  medium: "git",
  roles: [],
  max_rounds: 3,
  deliver: "owner",
  repo: "/code/mono",
} as Playbook;
const repo: Repository = {
  id: "r1",
  name: "Mono",
  path: "/code/mono",
  check: "make check",
  areas: [
    { name: "web", paths: ["apps/web/**"] },
    { name: "api", paths: ["apps/api"] },
  ],
};
const team: Team = {
  id: "t1",
  name: "Core",
  template: "code",
  max_rounds: 3,
  roles: [
    { name: "Implementer", kinds: ["implementer"], engine: "claude" },
    { name: "Reviewer", kinds: ["reviewer"], engine: "codex" },
  ],
};
const project = (id: string, title: string, extra: Partial<Project> = {}) =>
  ({
    id,
    title,
    status: "active",
    brief: {},
    playbook,
    directories: ["/code/mono"],
    team: "t1",
    scope: { repositories: [{ id: "r1", areas: ["web"] }] },
    ...extra,
  }) as Project;
const state = (projects: Project[]) =>
  ({
    projects,
    repositories: [repo],
    teams: [team],
    members: [
      {
        id: "m1",
        name: "Rex",
        kinds: ["reviewer"],
        engine: "codex",
      },
    ],
  }) as unknown as State;

it("says a shared repository's change reaches every project using it", () => {
  render(
    <RepositoriesSettings
      state={state([project("p1", "Web"), project("p2", "API")])}
      refresh={vi.fn(async () => {})}
    />,
  );
  expect(screen.getByRole("note").textContent).toMatch(
    /Shared by 2 projects \(Web, API\)/,
  );
  // A repository in use can't be removed.
  expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
});

it("saves a repository's settings, keeping its run recipe", async () => {
  const refresh = vi.fn(async () => {});
  const withRun = { ...repo, run: { start: "npm start", url: "u" } };
  render(
    <RepositoriesSettings
      state={{ ...state([project("p1", "Web")]), repositories: [withRun] }}
      refresh={refresh}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  fireEvent.change(screen.getByLabelText(/^Check/), {
    target: { value: "make test" },
  });
  fireEvent.change(screen.getByLabelText(/^Ignored folders/), {
    target: { value: "**/node_modules, vendor" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(refresh).toHaveBeenCalled());
  expect(calls[0].path).toBe("/api/repositories/r1");
  const body = JSON.parse(String(calls[0].options?.body));
  expect(body.check).toBe("make test");
  expect(body.prepare).toEqual(["**/node_modules", "vendor"]);
  expect(body.run).toEqual({ start: "npm start", url: "u" });
  expect(body.areas).toEqual(repo.areas);
});

it("fills a team's role from its settings", async () => {
  const refresh = vi.fn(async () => {});
  render(
    <TeamsSettings state={state([project("p1", "Web")])} refresh={refresh} />,
  );
  expect(screen.getByText("Used by Web.")).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Role"), {
    target: { value: "reviewer" },
  });
  fireEvent.change(screen.getByLabelText("Filled by"), {
    target: { value: "m1" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Give the role" }));
  await waitFor(() => expect(refresh).toHaveBeenCalled());
  expect(calls[0].path).toBe("/api/teams/t1/seats/reviewer");
  expect(JSON.parse(String(calls[0].options?.body))).toEqual({ member: "m1" });
});

it("chooses a project's team, repository and areas, and its overrides", async () => {
  const refresh = vi.fn(async () => {});
  const shared = state([project("p1", "Web"), project("p2", "API")]);
  render(
    <ProjectSetupCard
      project={shared.projects[0]}
      state={shared}
      refresh={refresh}
    />,
  );
  expect(screen.getAllByRole("note").map((n) => n.textContent)).toEqual([
    "This team is shared with API: changes here reach it too.",
    "This repository is shared with API: changes here reach it too.",
  ]);
  fireEvent.click(screen.getByRole("button", { name: "Change" }));
  fireEvent.click(screen.getByLabelText(/^api/));
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(refresh).toHaveBeenCalledTimes(1));
  expect(calls[0].path).toBe("/api/projects/p1/setup");
  expect(JSON.parse(String(calls[0].options?.body))).toEqual({
    team: "t1",
    repository: "r1",
    areas: ["web", "api"],
  });
  fireEvent.change(screen.getByLabelText("Change"), {
    target: { value: "exclude" },
  });
  fireEvent.change(screen.getByLabelText("Role"), {
    target: { value: "researcher" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Add override" }));
  await waitFor(() => expect(refresh).toHaveBeenCalledTimes(2));
  expect(calls[1].path).toBe("/api/projects/p1/seat-overrides");
  expect(JSON.parse(String(calls[1].options?.body))).toEqual({
    overrides: [{ action: "exclude", kind: "researcher" }],
  });
});

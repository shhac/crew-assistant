// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { ProjectLinear } from "./ProjectLinear";
import { LinearSources } from "./LinearSources";
import { BoardCard } from "./BoardCard";
import type { Project, Task, LinearLink, LinearRef } from "./api";
import boardCSS from "./styles/board.css?raw";
const team = "11111111-1111-1111-1111-111111111111";
const link: LinearLink = {
  connection_id: "lin",
  profile: "home",
  kind: "team",
  id: team,
  name: "Engineering",
  rules: { pick_up: true, states: ["Todo"], assignee: "unassigned", users: [] },
};
const project: Project = {
  id: "p",
  title: "Work",
  status: "active",
  brief: { version: 1, goal: "Work", criteria: [] },
};
const source: LinearRef = {
  kind: "issue",
  connection_id: "lin",
  profile: "home",
  id: "issue",
  identifier: "EX-1",
  title: "Do work",
  url: "https://linear.app/example/issue/EX-1",
  by: "assistant",
  at: "2026-09-30",
};
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("describes the rules and errors in the read view", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({ ok: true, json: async () => ({ connections: [] }) })),
  );
  render(
    <ProjectLinear
      project={{ ...project, linear: { ...link, last_error: "Read failed" } }}
      refresh={vi.fn()}
    />,
  );
  expect(screen.getByText("Pick up unassigned issues in Todo.")).toBeTruthy();
  expect(screen.getByRole("alert").textContent).toBe("Read failed");
});
it("uses only lin connections and loads and saves the chosen account's rules", async () => {
  const fetch = vi.fn(async (url: string) => ({
    ok: true,
    json: async () =>
      url === "/api/config"
        ? {
            connections: [
              {
                id: "lin",
                name: "Linear home",
                tool: "lin",
                profiles: ["home"],
              },
              {
                id: "slack",
                name: "Slack",
                tool: "agent-slack",
                profiles: ["home"],
              },
            ],
          }
        : url.includes("/teams?")
          ? [{ id: team, name: "Engineering" }]
          : url.includes("/states?")
            ? [{ id: "state", name: "Todo" }]
            : url.includes("/users?")
              ? [{ id: team, name: "Alice" }]
              : {},
  }));
  vi.stubGlobal("fetch", fetch);
  const refresh = vi.fn(async () => {});
  render(<ProjectLinear project={project} refresh={refresh} />);
  fireEvent.click(screen.getByText("Edit"));
  await screen.findByText("Linear home");
  expect(screen.queryByText("Slack")).toBeNull();
  fireEvent.change(screen.getByLabelText("Connection"), {
    target: { value: "lin" },
  });
  fireEvent.change(screen.getByLabelText("Account"), {
    target: { value: "home" },
  });
  await screen.findByText("Engineering");
  fireEvent.change(screen.getByLabelText("Team"), { target: { value: team } });
  await screen.findByText("Todo");
  fireEvent.change(screen.getByLabelText("Statuses (none means any)"), {
    target: { value: "Todo" },
  });
  fireEvent.click(
    screen.getByLabelText("Pick up matching issues every five minutes"),
  );
  fireEvent.click(screen.getByText("Save"));
  await waitFor(() => expect(refresh).toHaveBeenCalledOnce());
  const saved = fetch.mock.calls.find(
    (c) => c[0] === "/api/projects/p/linear",
  ) as unknown as [string, RequestInit];
  expect(saved[1].method).toBe("PUT");
  expect(JSON.parse(saved[1].body as string)).toEqual(link);
  expect(
    fetch.mock.calls.some((c) => c[0].includes("/users?profile=home")),
  ).toBe(true);
  expect(
    fetch.mock.calls.some((c) =>
      c[0].includes(`/states?profile=home&team=${team}`),
    ),
  ).toBe(true);
});
it("renders source links in the task details and board card", () => {
  const task: Task = {
    id: "t",
    project_id: "p",
    objective: "Work",
    criteria: [],
    status: "queued",
    stage: "todo",
    round: 0,
    revisions: [],
    verdicts: [],
    created_at: "2026-09-30",
    linear: [source],
  };
  render(
    <>
      <LinearSources links={task.linear} />
      <BoardCard task={task} project={project} />
    </>,
  );
  for (const name of ["EX-1", "EX-1: Do work"]) {
    const anchor = screen.getByRole("link", { name });
    expect(anchor.getAttribute("href")).toBe(source.url);
    expect(anchor.getAttribute("target")).toBe("_blank");
  }
  const marker = screen.getByRole("link", { name: "EX-1" });
  expect(marker.classList.contains("board-card-source-link")).toBe(true);
  const rule = boardCSS.match(/\.board-card-source-link\s*\{([^}]+)\}/)?.[1];
  expect(rule).toMatch(/position:\s*relative/);
  expect(rule).toMatch(/z-index:\s*1/);
});

it("caches account lists and loads only the chosen project's teams and states", async () => {
  const other = "22222222-2222-2222-2222-222222222222";
  const projectID = "33333333-3333-3333-3333-333333333333";
  const fetch = vi.fn(async (url: string) => ({
    ok: true,
    json: async () =>
      url === "/api/config"
        ? {
            connections: [
              {
                id: "lin",
                name: "Linear home",
                tool: "lin",
                profiles: ["home"],
              },
            ],
          }
        : url.includes("/project-teams?")
          ? [{ id: team, name: "Engineering" }]
          : url.includes("/projects?")
            ? [{ id: projectID, name: "Launch" }]
            : url.includes("/teams?")
              ? [
                  { id: team, name: "Engineering" },
                  { id: other, name: "Unrelated" },
                ]
              : url.includes("/states?")
                ? [{ id: "state", name: "Todo" }]
                : url.includes("/users?")
                  ? [{ id: team, name: "Alice" }]
                  : [],
  }));
  vi.stubGlobal("fetch", fetch);
  render(
    <ProjectLinear
      project={{ ...project, linear: link }}
      refresh={vi.fn(async () => {})}
    />,
  );
  fireEvent.click(screen.getByText("Edit"));
  await screen.findByText("Todo");
  fireEvent.change(screen.getByLabelText("Link to"), {
    target: { value: "project" },
  });
  await screen.findByText("Launch");
  fireEvent.change(screen.getByLabelText("Project"), {
    target: { value: projectID },
  });
  await screen.findByText("Todo");
  fireEvent.change(screen.getByLabelText("Link to"), {
    target: { value: "team" },
  });
  fireEvent.change(screen.getByLabelText("Team"), { target: { value: team } });
  await screen.findByText("Todo");
  for (const kind of ["teams", "projects", "users", "states"])
    expect(
      fetch.mock.calls.filter((c) => c[0].includes(`/linear/${kind}?`)),
    ).toHaveLength(1);
  expect(
    fetch.mock.calls.filter((c) => c[0].includes("/project-teams?")),
  ).toHaveLength(1);
  expect(fetch.mock.calls.some((c) => c[0].includes(`team=${other}`))).toBe(
    false,
  );
});

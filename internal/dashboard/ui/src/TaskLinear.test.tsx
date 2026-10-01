// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { TaskLinear } from "./ProjectLinear";
import type { Project, Task, LinearRef } from "./api";
const source: LinearRef = {
  id: "source",
  identifier: "EX-1",
  title: "Source",
  url: "https://linear.app/issue/EX-1",
  kind: "issue",
  connection_id: "lin",
  profile: "home",
  by: "assistant",
  at: "2026-10-01",
};
const added: LinearRef = {
  ...source,
  id: "added",
  identifier: "EX-2",
  title: "Added",
  url: "https://linear.app/issue/EX-2",
  by: "owner",
};
const project = {
  id: "p",
  title: "Work",
  linear: { connection_id: "lin", profile: "home" },
} as Project;
const task = { id: "t", linear: [source], linear_links: [added] } as Task;
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function fakeFetch() {
  const fetch = vi.fn(async (url: string) => ({
    ok: true,
    json: async () =>
      url === "/api/config"
        ? {
            connections: [
              { id: "other", name: "Other", tool: "lin", profiles: ["work"] },
              {
                id: "lin",
                name: "Home",
                tool: "lin",
                profiles: ["home", "extra"],
              },
            ],
          }
        : url.includes("/linear/projects?")
          ? [{ id: "project-id", name: "Project" }]
          : {},
  }));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
it("lists provenance and added external links, and removes only added links", async () => {
  const fetch = fakeFetch();
  const refresh = vi.fn(async () => {});
  render(<TaskLinear project={project} task={task} refresh={refresh} />);
  const links = screen.getAllByRole("link");
  expect(links.map((l) => l.getAttribute("href"))).toEqual([
    source.url,
    added.url,
  ]);
  for (const l of links) {
    expect(l.getAttribute("target")).toBe("_blank");
    expect(l.getAttribute("rel")).toBe("noopener noreferrer");
  }
  expect(screen.getByText(/Source issue/)).toBeTruthy();
  expect(screen.getAllByRole("button", { name: "Remove" })).toHaveLength(1);
  fireEvent.click(screen.getByRole("button", { name: "Remove" }));
  await waitFor(() =>
    expect(fetch).toHaveBeenCalledWith(
      "/api/projects/p/tasks/t/linear/issue/added",
      expect.objectContaining({ method: "DELETE" }),
    ),
  );
  await waitFor(() => expect(refresh).toHaveBeenCalled());
});
it("defaults to the project's connection and posts issue URLs and chosen projects", async () => {
  const fetch = fakeFetch();
  const refresh = vi.fn(async () => {});
  render(<TaskLinear project={project} task={task} refresh={refresh} />);
  await waitFor(() =>
    expect(
      (screen.getByLabelText("Connection") as HTMLSelectElement).value,
    ).toBe("lin"),
  );
  expect((screen.getByLabelText("Account") as HTMLSelectElement).value).toBe(
    "home",
  );
  fireEvent.change(screen.getByLabelText("Issue identifier or URL"), {
    target: { value: "https://linear.app/issue/EX-3" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Add link" }));
  await waitFor(() =>
    expect(fetch).toHaveBeenCalledWith(
      "/api/projects/p/tasks/t/linear",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          connection_id: "lin",
          profile: "home",
          kind: "issue",
          ref: "https://linear.app/issue/EX-3",
        }),
      }),
    ),
  );
  await waitFor(() => expect(refresh).toHaveBeenCalledTimes(1));
  fireEvent.change(screen.getByLabelText("Link to"), {
    target: { value: "project" },
  });
  await screen.findByRole("option", { name: "Project" });
  fireEvent.change(screen.getByLabelText("Project"), {
    target: { value: "project-id" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Add link" }));
  await waitFor(() =>
    expect(fetch).toHaveBeenCalledWith(
      "/api/projects/p/tasks/t/linear",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          connection_id: "lin",
          profile: "home",
          kind: "project",
          ref: "project-id",
        }),
      }),
    ),
  );
});
it("shows failed reads through ErrorNotice", async () => {
  fakeFetch();
  render(
    <TaskLinear
      project={project}
      task={task}
      refresh={vi.fn(async () => {})}
    />,
  );
  await screen.findByLabelText("Connection");
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => ({
      ok: !url.endsWith("/linear"),
      json: async () => ({ error: "Linear read failed" }),
    })),
  );
  fireEvent.change(screen.getByLabelText("Issue identifier or URL"), {
    target: { value: "EX-3" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Add link" }));
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Linear read failed",
  );
});

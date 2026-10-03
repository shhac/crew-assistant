// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ReleaseSettings, ReleasedMeta } from "./ReleaseSettings";
import type { Playbook, Project, ReleaseRecord } from "./api";
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
const playbook = (extra: Partial<Playbook> = {}) =>
  ({
    template: "code",
    medium: "git",
    roles: [],
    max_rounds: 3,
    deliver: "owner",
    land: { via: "push", target: "main" },
    ...extra,
  }) as Playbook;
const project = (pb: Playbook) => ({ id: "p1", playbook: pb }) as Project;
const release: ReleaseRecord = {
  version: "v1.1.0",
  commit: "abc",
  notes: "Adds a feature.\n\nFixes a warning.",
  at: "2026-10-03T14:20:00Z",
  approved_by: "pm",
  published: "owner/repo",
};

it("opts in with trimmed owner settings and removes them", async () => {
  const pb = playbook();
  const refresh = vi.fn(async () => {});
  const view = render(
    <ReleaseSettings project={project(pb)} playbook={pb} refresh={refresh} />,
  );
  expect(screen.getByText(/This project never releases/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Set up releases" }));
  fireEvent.change(screen.getByLabelText(/^When to release/), {
    target: { value: " After features " },
  });
  fireEvent.change(screen.getByLabelText(/^How to check/), {
    target: { value: " make release-check VERSION={version} " },
  });
  fireEvent.change(screen.getByLabelText(/^GitHub repository/), {
    target: { value: " owner/repo " },
  });
  fireEvent.change(screen.getByLabelText(/^Who approves/), {
    target: { value: "pm" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(refresh).toHaveBeenCalledTimes(1));
  expect(calls[0].path).toBe("/api/projects/p1/release");
  expect(JSON.parse(String(calls[0].options?.body))).toEqual({
    when: "After features",
    check: "make release-check VERSION={version}",
    github: "owner/repo",
    approve: "pm",
  });
  const configured = playbook({ release: { when: "After features" } });
  view.rerender(
    <ReleaseSettings
      project={project(configured)}
      playbook={configured}
      refresh={refresh}
    />,
  );
  expect(screen.getByText("No release check")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  expect(
    screen.getByText(/A check or publishing step already under way finishes/),
  ).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Remove releases" }));
  await waitFor(() => expect(refresh).toHaveBeenCalledTimes(2));
  expect(JSON.parse(String(calls[1].options?.body))).toBeNull();
});

it("shows the repository field only for push landing", () => {
  for (const land of [
    { via: "branch" },
    { pull_requests: true, github: "owner/repo", target: "main" },
  ]) {
    const pb = playbook({ land });
    const view = render(
      <ReleaseSettings
        project={project(pb)}
        playbook={pb}
        refresh={async () => {}}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Set up releases" }));
    expect(screen.queryByLabelText(/^GitHub repository/)).toBeNull();
    view.unmount();
  }
});

it("shows notes, publication destinations, and successful header metadata", () => {
  const pb = playbook({ release: { when: "After features", approve: "pm" } });
  const p = {
    ...project(pb),
    releases: [
      release,
      {
        ...release,
        version: "v1.0.0",
        published: "",
        note: "tag not published",
      },
    ],
  };
  render(
    <>
      <ReleaseSettings project={p} playbook={pb} refresh={async () => {}} />
      <ReleasedMeta release={p.releases[0]} />
    </>,
  );
  expect(screen.getByText("Published to owner/repo")).toBeTruthy();
  expect(screen.getByText("Local only · tag not published")).toBeTruthy();
  expect(screen.getAllByText(/Adds a feature/)).toHaveLength(2);
  expect(screen.getByText(/Released v1.1.0/).textContent).not.toContain(
    "local only",
  );
});

it("omits an empty header and marks a local release", () => {
  const view = render(<ReleasedMeta />);
  expect(view.container.textContent).toBe("");
  view.rerender(<ReleasedMeta release={{ ...release, published: "" }} />);
  expect(screen.getByText(/Released v1.1.0/).textContent).toContain(
    "(local only)",
  );
});

it("expands notes when three lines overflow at the current width", () => {
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(100);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(50);
  const pb = playbook();
  const p = { ...project(pb), releases: [release] };
  render(
    <ReleaseSettings project={p} playbook={pb} refresh={async () => {}} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Show more" }));
  expect(screen.getByRole("button", { name: "Show less" })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Show less" }));
  expect(screen.getByRole("button", { name: "Show more" })).toBeTruthy();
});

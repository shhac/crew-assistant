// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { InboxPage } from "./InboxPage";
import { normalizeState, type Project, type Task } from "./api";
import { requestHref } from "./router";

afterEach(cleanup);

const project = (id: string, title: string): Project => ({
  id,
  title,
  status: "active",
  brief: { version: 1, goal: "", criteria: [] },
});

const todayAt = (minute: number) => {
  const at = new Date();
  at.setHours(12, minute, 0, 0);
  return at.toISOString();
};

const landed = (
  id: string,
  projectID: string,
  objective: string,
  updated: string,
  ref?: string,
): Task => ({
  id,
  ref,
  project_id: projectID,
  objective,
  criteria: [],
  status: "landed",
  stage: "done",
  round: 1,
  revisions: [],
  verdicts: [],
  updated_at: updated,
});

const state = normalizeState({
  projects: [project("p1", "Harness"), project("p2", "Assistant")],
  tasks: [
    landed("t1", "p1", "Retry the stream when it drops", todayAt(30), "LA-7"),
    landed("t2", "p2", "Show the team as one list", todayAt(50), "CA-33"),
    landed("t3", "p1", "Take the new model catalog", todayAt(40)),
    landed("t4", "p2", "Landed long ago", "2020-01-01T12:00:00Z"),
  ],
});

const renderInbox = () =>
  render(<InboxPage state={state} refresh={async () => {}} onNew={() => {}} />);

describe("what landed today in the inbox", () => {
  it("sums up by project and keeps the titles out of the way", () => {
    renderInbox();
    const card = screen.getByRole("region", { name: "Done today" });
    const toggle = within(card).getByRole("button");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(within(toggle).getByText("3 done today")).toBeTruthy();
    expect(within(toggle).getByText("Harness 2 · Assistant 1")).toBeTruthy();
    expect(within(card).queryAllByRole("link")).toHaveLength(0);
    expect(screen.queryByText("Show the team as one list")).toBeNull();
  });
  it("opens to one line per request, newest first, each linking to it", () => {
    renderInbox();
    const card = screen.getByRole("region", { name: "Done today" });
    fireEvent.click(within(card).getByRole("button"));
    expect(within(card).getByRole("button").getAttribute("aria-expanded")).toBe(
      "true",
    );
    const links = within(card).getAllByRole("link");
    expect(links.map((a) => a.getAttribute("href"))).toEqual([
      requestHref("p2", "t2"),
      requestHref("p1", "t3"),
      requestHref("p1", "t1"),
    ]);
    expect(within(links[0]).getByText("CA-33")).toBeTruthy();
    expect(
      within(links[0])
        .getByText("Show the team as one list")
        .getAttribute("title"),
    ).toBe("Show the team as one list");
    expect(within(links[0]).getByText("Assistant")).toBeTruthy();
    expect(links[1].textContent).toBe("Take the new model catalogHarness");
    expect(screen.queryByText("Landed long ago")).toBeNull();
  });
});

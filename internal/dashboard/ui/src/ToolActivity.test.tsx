// @vitest-environment jsdom
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { durationLabel, ToolActivity } from "./ToolActivity";
import type { ChatToolEvent } from "./api";

const step = (
  id: string,
  status: ChatToolEvent["status"] = "completed",
  overrides: Partial<ChatToolEvent> = {},
): ChatToolEvent => ({
  id,
  tool: `tool_${id}`,
  label: `Step ${id}`,
  status,
  started_at: "2026-09-17T09:00:00Z",
  ...overrides,
});

afterEach(() => {
  cleanup();
});

const group = () =>
  screen.getByRole("group", { name: "What the assistant did" });
const details = () =>
  group().querySelector<HTMLDetailsElement>("details.tools-summary")!;
const summary = () => details().querySelector("summary")?.textContent;
const listed = () =>
  Array.from(details().querySelectorAll("li")).map((li) => li.textContent);
const problems = () =>
  Array.from(group().querySelectorAll(":scope > ul.tools-problems > li")).map(
    (li) => li.textContent,
  );

describe("tool activity", () => {
  it("names the running step while it works", () => {
    render(
      <ToolActivity
        events={[step("1"), step("2"), step("3", "running")]}
        live
      />,
    );
    expect(summary()).toBe("Step 3…");
    expect(listed()).toEqual(["✓Step 1", "✓Step 2", "•Step 3 · Working"]);
  });

  it("says it is working when the running step has no label", () => {
    render(
      <ToolActivity events={[step("1", "running", { label: "" })]} live />,
    );
    expect(summary()).toBe("Working…");
    expect(listed()).toEqual(["•A step · Working"]);
  });

  it("shows the newest step while the turn is live between steps", () => {
    render(<ToolActivity events={[step("1"), step("2"), step("3")]} live />);
    expect(summary()).toBe("Step 3");
  });

  it("says it is working when the newest finished step has no label", () => {
    render(
      <ToolActivity events={[step("1", "completed", { label: "" })]} live />,
    );
    expect(summary()).toBe("Working");
  });

  it("summarises the work once the turn is done, without timing when none is known", () => {
    render(<ToolActivity events={[step("1"), step("2"), step("3")]} />);
    expect(summary()).toBe("Worked · 3 steps");
    expect(listed()).toEqual(["✓Step 1", "✓Step 2", "✓Step 3"]);
    expect(problems()).toEqual([]);
  });

  it("says how long the work took, from the first start to the last finish", () => {
    render(
      <ToolActivity
        events={[
          step("1"),
          step("2", "completed", {
            started_at: "2026-09-17T09:00:30Z",
            finished_at: "2026-09-17T09:01:05Z",
          }),
        ]}
      />,
    );
    expect(summary()).toBe("Worked for 1 min 5 s · 2 steps");
  });

  it("counts a single step in the singular", () => {
    render(<ToolActivity events={[step("1")]} />);
    expect(summary()).toBe("Worked · 1 step");
  });

  it("starts folded so the steps do not cost a click to ignore", () => {
    render(<ToolActivity events={[step("1"), step("2"), step("3")]} />);
    expect(details().open).toBe(false);
    expect(within(details()).getByText("Step 1")).toBeTruthy();
  });

  it("keeps a failure visible outside the fold, and counts it", () => {
    render(
      <ToolActivity
        events={[step("1"), step("2", "failed"), step("3"), step("4")]}
      />,
    );
    expect(summary()).toBe("Worked · 4 steps · 1 failed");
    // The fold still holds every step, in the order they happened.
    expect(listed()).toEqual([
      "✓Step 1",
      "!Step 2 · Failed",
      "✓Step 3",
      "✓Step 4",
    ]);
    // Outside the fold, so it is seen without opening anything.
    expect(problems()).toEqual(["!Step 2 · Failed"]);
    expect(details().contains(group().querySelector("ul.tools-problems"))).toBe(
      false,
    );
  });

  it("keeps an interrupted step visible and says its outcome is unconfirmed", () => {
    render(<ToolActivity events={[step("1"), step("2", "interrupted")]} />);
    expect(problems()).toEqual(["!Step 2 · Stopped; outcome not confirmed"]);
    // Not known to have failed, so it is not counted as a failure.
    expect(summary()).toBe("Worked · 2 steps · 1 not confirmed");
  });

  it("keeps problems visible while another step is still running", () => {
    render(
      <ToolActivity
        events={[step("1", "failed"), step("2", "running")]}
        live
      />,
    );
    expect(summary()).toBe("Step 2…");
    expect(problems()).toEqual(["!Step 1 · Failed"]);
  });

  it("never shows the raw tool name", () => {
    render(
      <ToolActivity
        events={[step("1"), step("2", "failed"), step("3", "running")]}
        live
      />,
    );
    expect(group().textContent).not.toContain("tool_");
  });

  it("renders nothing without steps", () => {
    const { container } = render(<ToolActivity events={[]} />);
    expect(container.innerHTML).toBe("");
  });
});

describe("durationLabel", () => {
  it("leaves out anything that rounds to no time", () => {
    expect(durationLabel(0)).toBe("");
    expect(durationLabel(0.4)).toBe("");
    expect(durationLabel(0.6)).toBe("1 s");
  });

  it("rounds before splitting minutes, so it never says 60 s", () => {
    expect(durationLabel(59.6)).toBe("1 min");
    expect(durationLabel(119.6)).toBe("2 min");
  });

  it("gives seconds under a minute", () => {
    expect(durationLabel(1)).toBe("1 s");
    expect(durationLabel(42.4)).toBe("42 s");
  });

  it("gives minutes, with any remaining seconds", () => {
    expect(durationLabel(60)).toBe("1 min");
    expect(durationLabel(65)).toBe("1 min 5 s");
    expect(durationLabel(600)).toBe("10 min");
  });
});

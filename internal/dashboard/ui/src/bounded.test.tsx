// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import requestCSS from "./styles/request.css?raw";
import { useNewestInView } from "./ui";
import { atBottom, layOutScrolling } from "./testScroll";

function List({
  items,
  newest,
}: {
  items: string[];
  newest: "top" | "bottom";
}) {
  const box = useNewestInView<HTMLOListElement>(newest);
  return (
    <ol className="bounded" aria-label="Entries" {...box}>
      {items.map((item) => (
        <li key={item}>{item}</li>
      ))}
    </ol>
  );
}

const entries = (n: number) =>
  Array.from({ length: n }, (_, i) => `Entry ${i + 1}`);

let undo = () => {};
beforeEach(() => {
  undo = layOutScrolling();
});
afterEach(() => {
  cleanup();
  undo();
});

describe("a list kept in a box of its own", () => {
  it("opens at the bottom of a list kept oldest first, and follows it while left there", () => {
    const view = render(<List items={entries(20)} newest="bottom" />);
    const list = screen.getByRole("list", { name: "Entries" });
    expect(list.tabIndex).toBe(0);
    expect(atBottom(list)).toBe(true);
    view.rerender(<List items={entries(25)} newest="bottom" />);
    expect(atBottom(list)).toBe(true);
    expect(list.children).toHaveLength(25);
  });

  it("leaves a box where the owner scrolled it when more arrives", () => {
    const view = render(<List items={entries(20)} newest="bottom" />);
    const list = screen.getByRole("list", { name: "Entries" });
    list.scrollTop = 80;
    fireEvent.scroll(list);
    view.rerender(<List items={entries(25)} newest="bottom" />);
    expect(list.scrollTop).toBe(80);
    // Back at the end, it follows again.
    list.scrollTop = list.scrollHeight;
    fireEvent.scroll(list);
    view.rerender(<List items={entries(30)} newest="bottom" />);
    expect(atBottom(list)).toBe(true);
  });

  it("opens at the top of a list kept newest first, and stays put there", () => {
    const view = render(<List items={entries(20)} newest="top" />);
    const list = screen.getByRole("list", { name: "Entries" });
    expect(list.scrollTop).toBe(0);
    list.scrollTop = 120;
    fireEvent.scroll(list);
    view.rerender(<List items={entries(25)} newest="top" />);
    expect(list.scrollTop).toBe(120);
  });

  it("opens at the newest once there is something to show", () => {
    const view = render(<List items={[]} newest="bottom" />);
    const list = screen.getByRole("list", { name: "Entries" });
    expect(list.scrollTop).toBe(0);
    view.rerender(<List items={entries(20)} newest="bottom" />);
    expect(atBottom(list)).toBe(true);
  });
});

/** The declarations of one rule of a stylesheet, as written. */
function rule(css: string, selector: string) {
  const at = css.indexOf(`\n${selector} {`);
  expect(at, `${selector} is in the stylesheet`).toBeGreaterThanOrEqual(0);
  return css.slice(at, css.indexOf("}", at));
}

describe("the bounded box's style", () => {
  it("keeps the stage connector on the full list item, through padding and disclosures", () => {
    const track = rule(requestCSS, ".task-stage-flow > li");
    expect(track).toContain("var(--line)");
    expect(track).toMatch(/1px 100%\s+no-repeat/);
    expect(requestCSS).not.toContain(".task-stage-marker::after");
    expect(requestCSS).not.toContain(".task-stage-flow > li + li");
    expect(rule(requestCSS, ".task-stage-flow > li:first-child")).toContain(
      "calc(100% - 24px)",
    );
    expect(rule(requestCSS, ".task-stage-flow > li:last-child")).toContain(
      "1px 24px",
    );
  });
  it("has a height of its own that scrolls by itself, on any screen", () => {
    const box = rule(requestCSS, ".bounded");
    expect(box).toContain("max-height: min(60vh, 560px);");
    expect(box).toContain("max-height: min(60dvh, 560px);");
    expect(box).toContain("overflow-y: auto;");
    expect(box).toContain("overscroll-behavior: contain;");
    expect(box).toContain("min-width: 0;");
  });

  it("wraps long entries rather than widening the page", () => {
    expect(rule(requestCSS, ".bounded")).toContain("overflow-wrap: anywhere;");
    for (const selector of [".thread-text", ".bounded pre", ".bounded table"])
      expect(rule(requestCSS, selector)).toMatch(
        /overflow-wrap: anywhere;|max-width: 100%;/,
      );
    expect(requestCSS).toMatch(
      /\.check-note,\n\.verdict-evidence \{[^}]*overflow-wrap: anywhere;/,
    );
    expect(requestCSS).toMatch(
      /\.findings,[^{]*\.plan-list,[^{]*\{[^}]*overflow-wrap: anywhere;/,
    );
    expect(rule(requestCSS, ".relation-add .field")).toContain("min-width: 0;");
  });
});

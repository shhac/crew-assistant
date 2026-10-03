// @vitest-environment jsdom
import { render, screen, cleanup } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { Timeline } from "./MemberTimeline";

afterEach(cleanup);
it("renders a browser fallback note as a plain muted line", () => {
  render(
    <Timeline
      steps={[
        {
          id: 1,
          task_id: "task",
          seat: "Ada",
          role: "implementer",
          turn: "turn",
          item: "note",
          at: "2026-10-03T10:00:00Z",
          kind: "note",
          text: "Ran without the browser. No bridge is declared.",
        },
      ]}
      messages={[]}
      seat="Ada"
      loaded
      renderMessage={() => null}
    />,
  );
  expect(
    screen.getByText("Ran without the browser. No bridge is declared.")
      .className,
  ).toContain("muted");
  expect(screen.queryByText("A tool")).toBeNull();
});

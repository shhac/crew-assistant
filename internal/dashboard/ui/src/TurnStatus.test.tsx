// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { TurnStatus } from "./TurnStatus";
import type { VisibleTurn } from "./chatTurns";
afterEach(cleanup);
it("keeps the browser notice visible on a completed turn", () => {
  const props = {
    name: "Assistant",
    cancelling: new Set<string>(),
    onCancel: vi.fn(),
    onRestore: vi.fn(),
    onDiscard: vi.fn(),
    onRetry: vi.fn(),
  };
  const turn = {
    id: "t",
    message: "hello",
    status: "completed",
    browser_note: "Running without the browser. Chrome is not connected.",
  } as VisibleTurn;
  const view = render(<TurnStatus {...props} turn={turn} />);
  expect(screen.getByText(turn.browser_note!).className).toBe("muted small");
  view.rerender(
    <TurnStatus {...props} turn={{ ...turn, browser_note: undefined }} />,
  );
  expect(screen.queryByText(turn.browser_note!)).toBeNull();
});

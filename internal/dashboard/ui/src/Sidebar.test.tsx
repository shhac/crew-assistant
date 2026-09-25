// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { Sidebar } from "./Sidebar";
import { normalizeState } from "./api";

afterEach(cleanup);

const sidebar = (stopping: boolean, paused = false) =>
  render(
    <Sidebar
      state={normalizeState({ stopping, paused })}
      route={{ page: "inbox" }}
      needs={0}
      offline={false}
      chatOpen={false}
      onChat={() => {}}
      pausing={false}
      pauseError=""
      onPause={() => {}}
    />,
  );

describe("Sidebar status", () => {
  it("says the daemon is stopping and offers no pause meanwhile", () => {
    sidebar(true, true);
    expect(screen.getByText("Stopping")).toBeTruthy();
    expect(screen.getByText(/then crew-assistant stops/)).toBeTruthy();
    expect(screen.queryByText(/Steps already running finish/)).toBeNull();
    const pause = screen.getByRole("button", { name: "Resume teams" });
    expect((pause as HTMLButtonElement).disabled).toBe(true);
  });

  it("says running otherwise", () => {
    sidebar(false);
    expect(screen.getByText("Running")).toBeTruthy();
    expect(
      (
        screen.getByRole("button", {
          name: "Pause all teams",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false);
  });
});

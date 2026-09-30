// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { RequestPlan } from "./RequestPlan";
import type { Plan } from "./api";

afterEach(cleanup);
const plan: Plan = { summary: "Add provider icons.", role: "Researcher", at: "",
  changes: ["one", "two", "three", "four"] };

it("shows the missing designer notice even when the plan is folded", () => {
  render(<RequestPlan plan={{ ...plan, needs_designer: "New icons need drawings." }} />);
  expect(screen.getByRole("note").textContent).toBe(
    "Needs visual design, but no designer is on the team: New icons need drawings.",
  );
  expect(screen.getByText("Show the plan")).toBeTruthy();
});

it("shows no notice for old plans or ordinary work", () => {
  render(<RequestPlan plan={plan} />);
  expect(screen.queryByRole("note")).toBeNull();
});

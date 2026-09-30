// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { TeamPage } from "./TeamPage";
import { normalizeState, type Member } from "./api";

const member = (
  id: string,
  name: string,
  kinds: Member["kinds"],
  engine: string,
  created_at?: string,
): Member => ({ id, name, kinds, engine, learnings: [], created_at });

const state = normalizeState({
  assistant: { name: "Iris", personality: "" },
  members: [
    member("m1", "Zed", ["implementer"], "codex", "2026-09-01T10:00:00Z"),
    member("m2", "Ada", ["reviewer"], "claude", "2026-09-10T10:00:00Z"),
    member("m3", "Bea", ["qa", "researcher"], "claude", "2026-09-20T10:00Z"),
    member("m4", "Cy", ["pm"], "codex"),
  ],
});

const page = () => render(<TeamPage state={state} refresh={async () => {}} />);

const names = () =>
  Array.from(document.querySelectorAll(".member-list .member-name")).map(
    (n) => n.textContent,
  );

const sortBy = (label: string) =>
  fireEvent.change(screen.getByLabelText("Sort by"), {
    target: {
      value: Array.from(
        screen.getByLabelText<HTMLSelectElement>("Sort by").options,
      ).find((o) => o.text === label)?.value,
    },
  });

beforeEach(() => localStorage.clear());
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("TeamPage members", () => {
  it("offers every role to show, starting with all of them by name", () => {
    page();
    const show = screen.getByRole("group", { name: "Show" });
    expect(
      Array.from(show.querySelectorAll("button")).map((b) => b.textContent),
    ).toEqual([
      "All roles",
      "Researcher",
      "Designer",
      "Implementer",
      "Reviewer",
      "QA",
      "PM",
    ]);
    expect(
      screen
        .getByRole("button", { name: "All roles" })
        .getAttribute("aria-pressed"),
    ).toBe("true");
    expect(names()).toEqual(["Ada", "Bea", "Cy", "Zed"]);
  });

  it("shows only the members holding a role, including ones with several", () => {
    page();
    fireEvent.click(screen.getByRole("button", { name: "QA" }));
    expect(names()).toEqual(["Bea"]);
    fireEvent.click(screen.getByRole("button", { name: "Researcher" }));
    expect(names()).toEqual(["Bea"]);
    fireEvent.click(screen.getByRole("button", { name: "Reviewer" }));
    expect(names()).toEqual(["Ada"]);
    expect(
      screen
        .getByRole("button", { name: "Reviewer" })
        .getAttribute("aria-pressed"),
    ).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: "All roles" }));
    expect(names()).toHaveLength(4);
  });

  it("says so when no member holds the role shown", () => {
    page();
    fireEvent.click(screen.getByRole("button", { name: "Designer" }));
    expect(names()).toEqual([]);
    expect(
      screen.getByText("No member holds the designer role yet."),
    ).toBeTruthy();
  });

  it("sorts by role in team order, by engine, and newest first", () => {
    page();
    sortBy("Role");
    expect(names()).toEqual(["Bea", "Zed", "Ada", "Cy"]);
    sortBy("Engine");
    expect(names()).toEqual(["Ada", "Bea", "Cy", "Zed"]);
    sortBy("Newest first");
    expect(names()).toEqual(["Bea", "Ada", "Zed", "Cy"]);
    sortBy("Name (A–Z)");
    expect(names()).toEqual(["Ada", "Bea", "Cy", "Zed"]);
  });

  it("keeps the role shown and the order in this browser", () => {
    page();
    fireEvent.click(screen.getByRole("button", { name: "Implementer" }));
    sortBy("Newest first");
    cleanup();
    page();
    expect(
      screen
        .getByRole("button", { name: "Implementer" })
        .getAttribute("aria-pressed"),
    ).toBe("true");
    expect(screen.getByLabelText<HTMLSelectElement>("Sort by").value).toBe(
      "newest",
    );
    expect(names()).toEqual(["Zed"]);
  });

  it("ignores a remembered choice it doesn't know", () => {
    localStorage.setItem("crew-assistant.team-role", "astronaut");
    localStorage.setItem("crew-assistant.team-sort", "height");
    page();
    expect(names()).toEqual(["Ada", "Bea", "Cy", "Zed"]);
  });

  it("works when this browser's storage can't be used", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("denied");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("denied");
    });
    page();
    expect(names()).toEqual(["Ada", "Bea", "Cy", "Zed"]);
    fireEvent.click(screen.getByRole("button", { name: "PM" }));
    sortBy("Engine");
    expect(names()).toEqual(["Cy"]);
  });
});

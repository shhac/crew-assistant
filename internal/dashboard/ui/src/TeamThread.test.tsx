import { describe, expect, it } from "vitest";
import { countRows, senderWords } from "./TeamThread";
import type { FlowRow } from "./taskFlow";
import type { TeamMessage } from "./api";

const message = (m: Partial<TeamMessage>): TeamMessage => ({
  id: "m",
  to: "QA",
  kind: "qa",
  from: "owner",
  text: "t",
  status: "waiting",
  direction: 0,
  ...m,
});

describe("who sent a message to the team", () => {
  it("names the owner, the assistant, or the teammate who handed over a pull request", () => {
    expect(senderWords(message({}))).toBe("You");
    expect(senderWords(message({ from: "assistant" }))).toBe("The assistant");
    expect(senderWords(message({ from: "Ada", for_pr: true }))).toBe(
      "Ada, about the pull request,",
    );
  });
});

describe("which stage row counts a seat's messages", () => {
  const row = (
    key: string,
    name: string,
    state: FlowRow["state"],
  ): FlowRow => ({
    key,
    kind: "implementer",
    label: key,
    role: { name, kinds: ["implementer"], engine: "claude" },
    state,
    support: "",
    previous: [],
  });
  it("is the seat's current row, then its next, then its last", () => {
    const counting = countRows([
      row("r1", "Ada", "done"),
      row("r2", "Ada", "working"),
      row("r3", "Ada", "not-started"),
      row("q1", "Quinn", "done"),
      row("q2", "Quinn", "not-started"),
      row("v1", "Rune", "done"),
      row("v2", "Rune", "done"),
    ]);
    expect(Object.fromEntries(counting)).toEqual({
      Ada: "r2",
      Quinn: "q2",
      Rune: "v2",
    });
  });
});

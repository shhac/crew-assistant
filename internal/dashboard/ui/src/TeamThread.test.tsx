import { describe, expect, it } from "vitest";
import { senderWords } from "./TeamThread";
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

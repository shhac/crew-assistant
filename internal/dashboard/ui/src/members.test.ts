import { describe, expect, it } from "vitest";
import { learnedBy, learningParts } from "./members";

describe("learnings", () => {
  const at = "2026-09-20T10:00:00Z";
  it("heads a learning with when it applies, then its text", () => {
    expect(
      learningParts({
        id: "l1",
        when: "Reviewing error handling",
        text: "Check every error is wrapped. Say where.",
        at,
      }),
    ).toEqual({
      heading: "Reviewing error handling",
      body: "Check every error is wrapped. Say where.",
    });
  });
  it("heads one without a when by its first sentence, without repeating it", () => {
    expect(
      learningParts({ id: "l1", text: "Run the linter first. It's fast.", at }),
    ).toEqual({ heading: "Run the linter first.", body: "It's fast." });
    expect(learningParts({ id: "l1", text: "Keep commits small", at })).toEqual(
      { heading: "Keep commits small", body: "" },
    );
    expect(
      learningParts({ id: "l1", text: "Use v1.2 of the API. Always.", at }),
    ).toEqual({ heading: "Use v1.2 of the API.", body: "Always." });
  });
  it("says who recorded a learning and where", () => {
    const l = { id: "l1", text: "x", at };
    expect(learnedBy({ ...l, source: "owner" }, "Ada", "Iris")).toBe(
      "You added this",
    );
    expect(learnedBy(l, "Ada", "Iris")).toBe("You added this");
    expect(learnedBy({ ...l, source: "assistant" }, "Ada", "Iris")).toBe(
      "Added by Iris",
    );
    expect(
      learnedBy({ ...l, source: "member" }, "Ada", "Iris", "Launch note"),
    ).toBe("Ada learned this on Launch note");
  });
});

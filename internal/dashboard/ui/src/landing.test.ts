import { describe, expect, it } from "vitest";
import {
  approvalText,
  approveLabel,
  landsBy,
  mergeMethod,
  pmCanDecide,
  pmLandingLine,
  reversibility,
  wayFor,
  whatHappens,
} from "./landing";
import type { Playbook, Task } from "./api";

const writing: Playbook = {
  template: "draft",
  medium: "documents",
  roles: [
    { name: "Writer", kinds: ["implementer"], engine: "claude" },
    { name: "Reviewer", kinds: ["reviewer"], engine: "codex" },
  ],
  max_rounds: 3,
  deliver: "owner",
};
const code = (
  land: Playbook["land"] = { via: "push", target: "main" },
): Playbook => ({
  ...writing,
  template: "code",
  medium: "git",
  roles: [
    { name: "Implementer", kinds: ["implementer"], engine: "claude" },
    { name: "Reviewer", kinds: ["reviewer"], engine: "codex" },
    { name: "QA", kinds: ["qa"], engine: "claude" },
  ],
  check: "make check",
  branch_prefix: "crew/",
  land,
});

describe("landing", () => {
  it("says how approved work leaves a project, and what approving does", () => {
    expect(landsBy(undefined)).toBe("No team yet");
    expect(landsBy(writing)).toBe("Stays on its page");
    expect(landsBy(code())).toBe("Fast-forward main");
    expect(
      landsBy(code({ via: "pull-request", target: "main", method: "merge" })),
    ).toBe("Pull request · merge");
    expect(landsBy(code({}))).toBe("New local branch");
    expect(approveLabel(code())).toBe("Land on main");
    expect(approveLabel(code({ via: "pull-request", target: "main" }))).toBe(
      "Open pull request",
    );
    expect(approveLabel(code({}))).toBe("Create branch");
    expect(approveLabel(writing)).toBe("Approve");
    expect(approveLabel({ ...writing, deliver_to: "/out" })).toBe(
      "Approve and copy",
    );
    expect(reversibility({ via: "push" })).toBe("Undoable with effort");
    expect(reversibility({ via: "pull-request" })).toBe(
      "Permanent once merged",
    );
    expect(reversibility({})).toBe("Undoable");
    expect(whatHappens(code())[0]).toContain("main moves forward");
    expect(
      whatHappens(
        code({ via: "pull-request", target: "main", github: "o/r" }),
      )[0],
    ).toBe("A pull request opens on o/r into main.");
    expect(whatHappens({ ...writing, deliver_to: "/out" })).toEqual([
      "The draft is copied into /out.",
    ]);
    expect(whatHappens(code({}))).toEqual([
      "A new branch starting crew/ is created in your repository.",
      "Nothing is pushed.",
    ]);
    expect(
      whatHappens(
        code({ via: "pull-request", target: "main", github: "o/r" }),
      )[2],
    ).toBe("It merges by squash once GitHub says it's approved and green.");
    // A way this dashboard doesn't know lands as a new branch.
    expect(landsBy(code({ via: "carrier-pigeon" }))).toBe("New local branch");
    expect(reversibility({ via: "carrier-pigeon" })).toBe("Undoable");
    expect(wayFor("carrier-pigeon")).toBeUndefined();
    expect(wayFor("push")?.method("squash")).toBe("fast-forward");
    expect(wayFor("pull-request")?.method("rebase")).toBe("rebase");
    expect(wayFor("branch")?.method("squash")).toBe("");
    expect(mergeMethod({ method: "merge" })).toBe("merge");
    expect(mergeMethod(undefined)).toBe("squash");
  });

  it("lets the PM decide only where a push lands the change", () => {
    expect(pmCanDecide("push")).toBe(true);
    expect(pmCanDecide("pull-request")).toBe(false);
    expect(pmCanDecide("branch")).toBe(false);
    expect(approvalText({ via: "push", target: "main" })).toBe(
      "You approve each change",
    );
    expect(approvalText({ via: "push", target: "main", approve: "none" })).toBe(
      "It lands once the checks pass",
    );
    expect(approvalText({ via: "push", target: "main", approve: "pm" })).toBe(
      "The PM decides, once it's signed off",
    );
    // A policy the server would refuse never reads as the PM's.
    expect(
      approvalText({ via: "pull-request", target: "main", approve: "pm" }),
    ).toBe("You approve each change");
    const byPM = whatHappens(
      code({ via: "push", target: "main", approve: "pm" }),
    );
    expect(byPM[0]).toContain("the PM lands it or holds it");
    expect(byPM[1]).toContain("main moves forward");
    expect(byPM.at(-1)).toContain("one commit or keeps the team's own commits");
    expect(byPM.at(-1)).toContain("branch is cleaned up");
    expect(whatHappens(code())[0]).toContain("main moves forward");
  });

  it("says what the PM decided about a change", () => {
    const task = (status: Task["status"], land: boolean): Task => ({
      id: "t1",
      project_id: "p1",
      objective: "Cache",
      criteria: null,
      status,
      stage: "done",
      round: 1,
      revisions: null,
      verdicts: null,
      land_decision: {
        by: "pm",
        land,
        reason: "ready first",
        revision: 2,
        at: "",
      },
    });
    expect(pmLandingLine(task("landed", true))).toBe(
      "Landed by the PM as one commit: ready first",
    );
    expect(pmLandingLine(task("landing", true))).toBe(
      "The PM is landing it as one commit: ready first",
    );
    const kept = task("landed", true);
    kept.land_decision = { ...kept.land_decision!, method: "fast-forward" };
    expect(pmLandingLine(kept)).toBe(
      "Landed by the PM keeping its commits: ready first",
    );
    expect(pmLandingLine(task("waiting", false))).toBe(
      "Held by the PM: ready first",
    );
    expect(
      pmLandingLine({ ...task("landed", true), land_decision: undefined }),
    ).toBe("");
  });
});

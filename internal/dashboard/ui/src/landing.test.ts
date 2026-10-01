import { describe, expect, it } from "vitest";
import {
  approveHint,
  landingInput,
  mergeHint,
  NO_PM_YET,
  openHint,
  approvalText,
  approveLabel,
  landsBy,
  mergeMethod,
  pmCanDecide,
  pmLandingLine,
  reversibility,
  wayFor,
  wayOf,
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
      landsBy(code({ pull_requests: true, target: "main", merge: "merge" })),
    ).toBe("Pull request · merge");
    expect(landsBy(code({}))).toBe("New local branch");
    expect(approveLabel(code())).toBe("Land on main");
    expect(approveLabel(code({ pull_requests: true, target: "main" }))).toBe(
      "Open pull request",
    );
    expect(approveLabel(code({}))).toBe("Create branch");
    expect(approveLabel(writing)).toBe("Approve");
    expect(approveLabel({ ...writing, deliver_to: "/out" })).toBe(
      "Approve and copy",
    );
    expect(reversibility({ via: "push" })).toBe("Undoable with effort");
    expect(reversibility({ pull_requests: true })).toBe(
      "Permanent once merged",
    );
    expect(reversibility({})).toBe("Undoable");
    expect(whatHappens(code())[0]).toContain("main moves forward");
    expect(
      whatHappens(
        code({
          pull_requests: true,
          target: "main",
          github: "o/r",
          open: "owner",
        }),
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
        code({
          pull_requests: true,
          target: "main",
          github: "o/r",
          open: "owner",
        }),
      )[2],
    ).toBe(
      "Once it's approved where review is asked for, green and every thread is resolved, the PM decides whether it merges once it's ready, by squash.",
    );
    // A way this dashboard doesn't know lands as a new branch.
    expect(landsBy(code({ via: "carrier-pigeon" }))).toBe("New local branch");
    expect(reversibility({ via: "carrier-pigeon" })).toBe("Undoable");
    expect(wayFor("carrier-pigeon")).toBeUndefined();
    expect(wayOf({ via: "push", pull_requests: true })).toBe("pull-request");
    expect(wayOf({ via: "push" })).toBe("push");
    expect(wayOf(undefined)).toBe("branch");
    expect(mergeMethod({ merge: "merge" })).toBe("merge");
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
    // With pull requests the PM decides which open, unless told otherwise.
    expect(approvalText({ pull_requests: true, target: "main" })).toBe(
      "The PM decides whether its pull request opens, once it's signed off; the PM decides whether it merges once it's ready",
    );
    expect(
      approvalText({ pull_requests: true, target: "main", open: "owner" }),
    ).toBe(
      "You approve each pull request before it opens; the PM decides whether it merges once it's ready",
    );
    expect(
      approvalText({
        pull_requests: true,
        target: "main",
        open: "implementer",
        approve: "none",
      }),
    ).toBe(
      "Its pull request opens once the checks pass; it merges as soon as it's ready",
    );
    expect(
      whatHappens(
        code({ pull_requests: true, target: "main", github: "o/r" }),
      )[0],
    ).toContain("the PM opens its pull request or holds it");
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

describe("the landing editor", () => {
  const form = {
    means: " merged ",
    via: "push",
    pullRequests: false,
    target: " main ",
    github: " o/r ",
    merge: "rebase",
    open: "owner",
    merging: "none",
    approve: "pm",
  };
  it("saves only what the way needs", () => {
    expect(landingInput(form)).toEqual({
      means: "merged",
      via: "push",
      target: "main",
      method: "fast-forward",
      pull_requests: false,
      github: "",
      merge: "",
      open: "",
      approve: "pm",
    });
    // With pull requests, who approves merging is what is saved.
    expect(landingInput({ ...form, pullRequests: true })).toMatchObject({
      pull_requests: true,
      github: "o/r",
      merge: "rebase",
      open: "owner",
      approve: "none",
    });
    // A branch has no target, and never leaves landing to the PM.
    expect(landingInput({ ...form, via: "branch" })).toMatchObject({
      target: "",
      method: "",
      approve: "before",
    });
  });
  it("hints at who decides, and asks the owner while the team has no PM", () => {
    expect(approveHint("branch", "before", true)).toContain(
      "a new branch lands nothing",
    );
    expect(approveHint("push", "before", true)).toBe("");
    expect(approveHint("push", "pm", false)).toBe(NO_PM_YET);
    expect(openHint("pm", false)).toBe(NO_PM_YET);
    expect(openHint("implementer", false)).toContain(
      "as soon as the reviewers and QA pass it",
    );
    expect(mergeHint("pm", false)).toContain(NO_PM_YET);
    expect(mergeHint("none", false)).not.toContain(NO_PM_YET);
  });
});

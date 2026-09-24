import { describe, expect, it } from "vitest";
import {
  approveLabel,
  landsBy,
  mergeMethod,
  reversibility,
  wayFor,
  whatHappens,
} from "./landing";
import type { Playbook } from "./api";

const writing: Playbook = {
  template: "draft",
  medium: "documents",
  roles: [
    { name: "Writer", kind: "implementer", engine: "claude" },
    { name: "Reviewer", kind: "reviewer", engine: "codex" },
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
    { name: "Implementer", kind: "implementer", engine: "claude" },
    { name: "Reviewer", kind: "reviewer", engine: "codex" },
    { name: "QA", kind: "qa", engine: "claude" },
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
});

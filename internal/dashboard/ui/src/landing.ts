import type { LandPolicy, Playbook, Task } from "./api";

/** Whether a team works on code; landing only has meaning for code. */
export const isCode = (playbook?: Playbook) => playbook?.medium === "git";

/** How a pull request merges, when the owner hasn't said. */
export const mergeMethod = (land?: LandPolicy) => land?.method || "squash";

export interface LandingWay {
  /** The way itself, as the landing settings name it. */
  label: string;
  landsBy: (land: LandPolicy) => string;
  reversibility: string;
  approve: (land: LandPolicy) => string;
  happens: (land: LandPolicy, playbook: Playbook) => string[];
  /** The merge method saved for this way, from the one the owner chose. */
  method: (chosen: string) => string;
}

export const landingWays: Record<string, LandingWay> = {
  branch: {
    label: "A new local branch",
    landsBy: () => "New local branch",
    reversibility: "Undoable",
    approve: () => "Create branch",
    happens: (_, playbook) => [
      `A new branch starting ${playbook.branch_prefix ?? "crew/"} is created in your repository.`,
      "Nothing is pushed.",
    ],
    method: () => "",
  },
  push: {
    label: "Fast-forward a branch",
    landsBy: (land) => `Fast-forward ${land.target}`,
    reversibility: "Undoable with effort",
    approve: (land) => `Land on ${land.target}`,
    happens: (land) => [
      `${land.target} moves forward to include this change. Nothing already on it is replaced.`,
      `If ${land.target} is checked out, your checkout updates too; if it has uncommitted changes, landing stops and asks you first.`,
    ],
    method: () => "fast-forward",
  },
  "pull-request": {
    label: "A pull request on GitHub",
    landsBy: (land) => `Pull request · ${mergeMethod(land)}`,
    reversibility: "Permanent once merged",
    approve: () => "Open pull request",
    happens: (land) => [
      `A pull request opens on ${land.github} into ${land.target}.`,
      "The team answers its reviews and fixes failing checks.",
      `It merges by ${mergeMethod(land)} once GitHub says it's approved and green.`,
    ],
    method: (chosen) => chosen,
  },
};

/** The way named, if this dashboard knows it. */
export const wayFor = (via?: string): LandingWay | undefined =>
  via && Object.hasOwn(landingWays, via) ? landingWays[via] : undefined;

/** How a policy lands; one with no way it knows lands as a new branch. */
const landingWay = (land?: LandPolicy) =>
  wayFor(land?.via) ?? landingWays.branch;

/** How a project's approved work leaves it, in a few words. */
export function landsBy(playbook?: Playbook): string {
  if (!playbook) return "No team yet";
  if (!isCode(playbook))
    return playbook.deliver_to ? "Copied to a folder" : "Stays on its page";
  return landingWay(playbook.land).landsBy(playbook.land ?? {});
}

export function reversibility(land?: LandPolicy): string {
  return landingWay(land).reversibility;
}

/** The approve button's words: what approving actually does. */
export function approveLabel(playbook?: Playbook): string {
  if (!playbook || !isCode(playbook))
    return playbook?.deliver_to ? "Approve and copy" : "Approve";
  return landingWay(playbook.land).approve(playbook.land ?? {});
}

/** What approving leads to, step by step. */
export function whatHappens(playbook?: Playbook): string[] {
  if (!playbook || !isCode(playbook))
    return playbook?.deliver_to
      ? [`The draft is copied into ${playbook.deliver_to}.`]
      : ["The draft stays here, marked approved."];
  const steps = landingWay(playbook.land).happens(
    playbook.land ?? {},
    playbook,
  );
  return pmDecides(playbook.land)
    ? [
        "Once the reviewers and QA pass it, nothing waits on you and what it depends on has landed, the PM lands it or holds it, and says why.",
        ...steps,
        "The PM chooses whether it lands as one commit or keeps the team's own commits, and the task's branch is cleaned up after.",
      ]
    : steps;
}

/**
 * Whether the PM can be given the decision to land: only a push lands
 * straight on the target. A pull request merges as GitHub's reviews say, and
 * a new branch lands nothing.
 */
export const pmCanDecide = (via?: string) => via === "push";

/** Whether the project's PM decides what lands. */
export const pmDecides = (land?: LandPolicy) =>
  land?.approve === "pm" && pmCanDecide(land.via);

/** Who approves a change before it lands, in words. */
export function approvalText(land?: LandPolicy): string {
  if (pmDecides(land)) return "The PM decides, once it's signed off";
  return land?.approve === "none"
    ? "It lands once the checks pass"
    : "You approve each change";
}

/** What the PM decided about landing a task's change, or "". */
export function pmLandingLine(task: Task): string {
  const decided = task.land_decision;
  if (!decided || decided.by !== "pm") return "";
  if (!decided.land) return `Held by the PM: ${decided.reason}`;
  const how =
    decided.method === "fast-forward" ? "keeping its commits" : "as one commit";
  return task.status === "landed"
    ? `Landed by the PM ${how}: ${decided.reason}`
    : `The PM is landing it ${how}: ${decided.reason}`;
}

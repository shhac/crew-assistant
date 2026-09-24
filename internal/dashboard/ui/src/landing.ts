import type { LandPolicy, Playbook } from "./api";

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
  return landingWay(playbook.land).happens(playbook.land ?? {}, playbook);
}

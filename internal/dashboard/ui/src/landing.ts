import type { LandingInput, LandPolicy, Playbook, Project, Task } from "./api";

/** Whether a team works on code; landing only has meaning for code. */
export const isCode = (playbook?: Playbook) => playbook?.medium === "git";

/** How a pull request merges, when the owner hasn't said. */
export const mergeMethod = (land?: LandPolicy) => land?.merge || "squash";

/** The way a policy lands: through pull requests when they're on. */
export const wayOf = (land?: LandPolicy) =>
  land?.pull_requests ? "pull-request" : land?.via || "branch";

export interface LandingWay {
  /** The way itself, as the landing settings name it. */
  label: string;
  landsBy: (land: LandPolicy) => string;
  reversibility: string;
  approve: (land: LandPolicy) => string;
  happens: (land: LandPolicy, playbook: Playbook) => string[];
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
  },
  "pull-request": {
    label: "A pull request on GitHub",
    landsBy: (land) => `Pull request · ${mergeMethod(land)}`,
    reversibility: "Permanent once merged",
    approve: () => "Open pull request",
    happens: (land) => [
      `A pull request opens on ${land.github} into ${land.target}.`,
      "The team answers its reviews and fixes failing checks.",
      `Once it's approved where review is asked for, green and every thread is resolved, ${mergeWords[mergeGate(land)] ?? mergeWords.pm}, by ${mergeMethod(land)}.`,
    ],
  },
};

/** The way named, if this dashboard knows it. */
export const wayFor = (via?: string): LandingWay | undefined =>
  via && Object.hasOwn(landingWays, via) ? landingWays[via] : undefined;

/** How a policy lands; one with no way it knows lands as a new branch. */
const landingWay = (land?: LandPolicy) =>
  wayFor(wayOf(land)) ?? landingWays.branch;

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
  if (playbook.land?.pull_requests)
    return pmDecides(playbook.land)
      ? [
          "Once the reviewers and QA pass it, nothing waits on you and what it depends on has landed, the PM opens its pull request or holds it, and says why.",
          ...steps,
        ]
      : steps;
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

/** Why a project's landing is paused, in a line, or "" when it isn't. */
export function landingPausedLine(project: Project): string {
  const paused = project.landing_paused;
  if (!paused) return "";
  return paused.reason ? `Landing paused: ${paused.reason}` : "Landing paused";
}

/** Who decides that a pull request opens, the PM unless set. */
export const openGate = (land?: LandPolicy) => land?.open || "pm";

/** Who approves a ready pull request merging, the PM unless set. */
export const mergeGate = (land?: LandPolicy) => land?.approve || "pm";

const mergeWords: Record<string, string> = {
  pm: "the PM decides whether it merges once it's ready",
  before: "you approve each merge once it's ready",
  none: "it merges as soon as it's ready",
};

/**
 * Whether the project's PM decides what goes out: what lands by push, or
 * which pull requests open.
 */
export const pmDecides = (land?: LandPolicy) =>
  land?.pull_requests
    ? openGate(land) === "pm"
    : land?.approve === "pm" && pmCanDecide(wayOf(land));

const openWords: Record<string, string> = {
  pm: "The PM decides whether its pull request opens, once it's signed off",
  owner: "You approve each pull request before it opens",
  implementer: "Its pull request opens once the checks pass",
};

/** Who approves a change before it goes out, in words. */
export function approvalText(land?: LandPolicy): string {
  if (land?.pull_requests)
    return `${openWords[openGate(land)] ?? openWords.owner}; ${mergeWords[mergeGate(land)] ?? mergeWords.pm}`;
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
  if (task.proposal?.merge_approved)
    return task.status === "landed"
      ? `Merged on the PM's word: ${decided.reason}`
      : `The PM is merging it: ${decided.reason}`;
  if (task.playbook?.land?.pull_requests)
    return `The PM opened its pull request: ${decided.reason}`;
  const how =
    decided.method === "fast-forward" ? "keeping its commits" : "as one commit";
  return task.status === "landed"
    ? `Landed by the PM ${how}: ${decided.reason}`
    : `The PM is landing it ${how}: ${decided.reason}`;
}

/** Said where the PM is to decide but the team has none to. */
export const NO_PM_YET =
  "The team has no PM yet, so you're asked until it has one.";

/** Who approves a change landing: only a push can leave it to the PM. */
export const effectiveApprove = (chosen: string, way: string) =>
  chosen === "pm" && !pmCanDecide(way) ? "before" : chosen;

/** The hint under who approves a change landing without pull requests. */
export function approveHint(
  way: string,
  approve: string,
  hasPM: boolean,
): string {
  if (!pmCanDecide(way))
    return "The PM can't decide here: a new branch lands nothing.";
  if (approve !== "pm") return "";
  return hasPM
    ? "Once the reviewers and QA pass a change, nothing waits on you and what it depends on has landed, the PM lands or holds it and says why. It lands a change as one commit or keeps the team's commits, and cleans up the branch. You can still land or stop it yourself."
    : NO_PM_YET;
}

const openHints: Record<string, string> = {
  pm: "Once the reviewers and QA pass a change, the PM opens its pull request or holds it and says why. You can still open it yourself.",
  owner:
    "You see the title and description the implementer wrote before it opens.",
  implementer:
    "A change opens its pull request as soon as the reviewers and QA pass it.",
};

/** The hint under who decides that a pull request opens. */
export function openHint(open: string, hasPM: boolean): string {
  if (open === "pm" && !hasPM) return NO_PM_YET;
  return openHints[open] ?? openHints.pm;
}

/** The hint under who approves a ready pull request merging. */
export function mergeHint(merging: string, hasPM: boolean): string {
  const ready =
    "Ready means approved where the repository asks for review, every check green, every review thread resolved, and no conflicts.";
  return merging === "pm" && !hasPM ? `${ready} ${NO_PM_YET}` : ready;
}

/** What the landing editor's choices are, as the owner made them. */
export interface LandingForm {
  means: string;
  via: string;
  pullRequests: boolean;
  target: string;
  github: string;
  merge: string;
  open: string;
  merging: string;
  approve: string;
}

/**
 * The landing policy the editor saves: only what the way needs, with who
 * approves merging standing in for who approves landing when pull requests
 * are on.
 */
export function landingInput(form: LandingForm): LandingInput {
  const way = form.pullRequests ? "pull-request" : form.via;
  return {
    means: form.means.trim(),
    via: form.via,
    target: way === "branch" ? "" : form.target.trim(),
    method: form.via === "push" ? "fast-forward" : "",
    pull_requests: form.pullRequests,
    github: form.pullRequests ? form.github.trim() : "",
    merge: form.pullRequests ? form.merge : "",
    open: form.pullRequests ? form.open : "",
    approve: form.pullRequests
      ? form.merging
      : effectiveApprove(form.approve, way),
  };
}

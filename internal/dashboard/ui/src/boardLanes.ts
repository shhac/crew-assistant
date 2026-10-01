import { isCode } from "./landing";
import { holds } from "./members";
import { finished, stageLabel } from "./stages";
import type { Project, Stage, Task } from "./api";

export interface Lane {
  stage: Stage;
  label: string;
}

/** A board column: one lane, or a stack of lanes that share the column. */
export interface Column {
  key: string;
  lanes: Lane[];
}

/**
 * Whether the board needs a lane for this kind of role: the team has it, or
 * a request that started with it is still open after the team changed.
 */
const hasLane = (project: Project, tasks: Task[], kind: string, stage: Stage) =>
  !!project.playbook?.roles.some((r) => holds(r, kind)) ||
  tasks.some(
    (t) =>
      !finished(t) &&
      (t.stage === stage || !!t.roles?.some((r) => holds(r, kind))),
  );

/**
 * The board's columns; triage, research, design and QA show only for teams
 * that have them. Triage sits below To do, in the same column. Work ready to
 * land sits above the board, not in a column.
 */
export function boardColumns(project: Project, tasks: Task[]): Column[] {
  const label = (stage: Stage) => stageLabel(stage, project.playbook);
  const lane = (kind: string, stage: Stage): Lane[] =>
    hasLane(project, tasks, kind, stage)
      ? [{ stage, label: label(stage) }]
      : [];
  const research = [
    ...lane("researcher", "researching"),
    ...lane("designer", "designing"),
  ];
  return [
    {
      key: "todo",
      lanes: [{ stage: "todo", label: label("todo") }, ...lane("pm", "triage")],
    },
    ...(research.length ? [{ key: "research", lanes: research }] : []),
    {
      key: "implementing",
      lanes: [{ stage: "implementing", label: label("implementing") }],
    },
    {
      key: "checks",
      lanes: [
        ...lane("qa", "qa"),
        { stage: "reviewing", label: label("reviewing") },
      ],
    },
  ];
}

/**
 * The rows above the columns, in the order work reaches them: for a project
 * landing through pull requests, the pull request still to open and the one
 * open but not yet ready, and then work ready to land. A pull request row
 * stays while a task that started with pull requests is in it.
 */
export function boardRows(project: Project, tasks: Task[]): Lane[] {
  const prs = !!project.playbook?.land?.pull_requests;
  const row = (stage: Stage): Lane[] =>
    prs || tasks.some((t) => !finished(t) && t.stage === stage)
      ? [{ stage, label: stageLabel(stage, project.playbook) }]
      : [];
  return [
    ...row("pr_opening"),
    ...row("pr_open"),
    { stage: "ready", label: readyLabel(project) },
  ];
}

/** What the board calls work that is ready to go out, kept above the columns. */
export const readyLabel = (project: Project) =>
  stageLabel("ready", project.playbook);

/** What the board calls finished work, kept apart from the columns. */
export const doneLabel = (project: Project) =>
  isCode(project.playbook) ? "Landed" : "Delivered";

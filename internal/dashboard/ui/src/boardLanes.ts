import { isCode } from "./landing";
import { holds } from "./members";
import { finished } from "./stages";
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
 * The board's columns; research, design and QA show only for teams that have
 * them. Work ready to land sits above the board, not in a column.
 */
export function boardColumns(project: Project, tasks: Task[]): Column[] {
  const lane = (kind: string, stage: Stage, label: string): Lane[] =>
    hasLane(project, tasks, kind, stage) ? [{ stage, label }] : [];
  const research = [
    ...lane("researcher", "researching", "Researching"),
    ...lane("designer", "designing", "Designing"),
  ];
  const implementing = isCode(project.playbook) ? "Implementing" : "Writing";
  return [
    { key: "todo", lanes: [{ stage: "todo", label: "To do" }] },
    ...(research.length ? [{ key: "research", lanes: research }] : []),
    {
      key: "implementing",
      lanes: [{ stage: "implementing", label: implementing }],
    },
    {
      key: "checks",
      lanes: [
        ...lane("qa", "qa", "QA"),
        { stage: "reviewing", label: "Reviewing" },
      ],
    },
  ];
}

/** What the board calls work that is ready to go out, kept above the columns. */
export const readyLabel = (project: Project) =>
  isCode(project.playbook) ? "Ready to land" : "Ready";

/** What the board calls finished work, kept apart from the columns. */
export const doneLabel = (project: Project) =>
  isCode(project.playbook) ? "Landed" : "Delivered";

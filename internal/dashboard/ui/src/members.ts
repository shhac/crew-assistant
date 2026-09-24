import { taskPlaybook } from "./stages";
import type { Member, MemberKind, Project, Role, Task } from "./api";

export const engines = [
  { id: "claude", label: "Claude" },
  { id: "codex", label: "Codex" },
];
export const engineLabel = (id: string) =>
  engines.find((e) => e.id === id)?.label ?? id;

export const memberKinds: { id: MemberKind; label: string }[] = [
  { id: "implementer", label: "Implementer" },
  { id: "reviewer", label: "Reviewer" },
  { id: "qa", label: "QA" },
];
export const kindLabel = (kind: string) =>
  memberKinds.find((k) => k.id === kind)?.label ?? kind;

/** "Implementer · Claude opus": what a member is and what it runs on. */
export const memberSummary = (m: Member) =>
  [
    kindLabel(m.kind),
    [engineLabel(m.engine), m.model].filter(Boolean).join(" "),
  ].join(" · ");

/** The open projects whose team has this member in a role. */
export const memberProjects = (member: Member, projects: Project[]) =>
  projects.filter(
    (p) =>
      p.status !== "completed" &&
      p.playbook?.roles.some((r) => r.member === member.id),
  );

/** A request's team: the one it started with, or else the project's. */
export const taskRoles = (task: Task, project: Project): Role[] =>
  task.roles?.length ? task.roles : (taskPlaybook(task, project)?.roles ?? []);

/** The member a role was copied from, while that member is still there. */
export const memberOf = (role: Role | undefined, members: Member[]) =>
  role?.member ? members.find((m) => m.id === role.member) : undefined;

/** The member behind the role of a request's team with this name. */
export function roleMember(
  roles: Role[] | undefined,
  name: string | undefined,
  members: Member[],
) {
  return memberOf(
    roles?.find((r) => r.name === name),
    members,
  );
}

/**
 * The member at work on a request now: the implementer while it writes,
 * the checker named while it is checked, and no one otherwise.
 */
export function atWork(task: Task, members: Member[]) {
  if (task.status === "writing")
    return memberOf(
      task.roles?.find((r) => r.kind === "implementer"),
      members,
    );
  if (task.status === "reviewing" || task.status === "deciding")
    return memberOf(
      task.roles?.find((r) => r.name === task.checking),
      members,
    );
  return undefined;
}

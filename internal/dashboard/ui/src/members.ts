import type { Member, MemberKind, Project, Role, Task } from "./api";

export const engines = [
  { id: "claude", label: "Claude" },
  { id: "codex", label: "Codex" },
];
export const engineLabel = (id: string) =>
  engines.find((e) => e.id === id)?.label ?? id;

/**
 * In the order a team works, then the PM who keeps its list; `word` is how a
 * kind reads mid-sentence.
 */
export const memberKinds: { id: MemberKind; label: string; word: string }[] = [
  { id: "planner", label: "Planner", word: "planner" },
  { id: "implementer", label: "Implementer", word: "implementer" },
  { id: "reviewer", label: "Reviewer", word: "reviewer" },
  { id: "qa", label: "QA", word: "QA" },
  { id: "pm", label: "PM", word: "PM" },
];
export const kindLabel = (kind: string) =>
  memberKinds.find((k) => k.id === kind)?.label ?? kind;
export const kindWord = (kind: string) =>
  memberKinds.find((k) => k.id === kind)?.word ?? kind;

/** Whether a seat or a member holds a kind of role. */
export const holds = (who: { kinds?: readonly string[] }, kind: string) =>
  !!who.kinds?.includes(kind);

/** Kinds held alongside a seat's work, rather than being it. */
const besideWork = new Set(["planner", "pm"]);

/**
 * What a seat does once the work has started; "" for one that only plans or
 * keeps the list.
 */
export const workingKind = (role: Role) =>
  role.kinds.find((k) => !besideWork.has(k)) ?? "";

/** "Planner and implementer": the kinds held, in the order a team works. */
export function kindsLabel(kinds: readonly string[]) {
  const rank = (kind: string) => {
    const i = memberKinds.findIndex((k) => k.id === kind);
    return i < 0 ? memberKinds.length : i;
  };
  const [first, ...rest] = [...kinds].sort((a, b) => rank(a) - rank(b));
  if (!first) return "";
  const words = [kindLabel(first), ...rest.map(kindWord)];
  if (words.length < 2) return words[0];
  return `${words.slice(0, -1).join(", ")} and ${words.at(-1)}`;
}

/**
 * Why a member can't hold these kinds, as the server would refuse them:
 * verdicts and messages name the seat, so it does one kind of work, and
 * planning and keeping the list sit alongside.
 */
export function kindsProblem(kinds: readonly string[]) {
  if (!kinds.length) return "Pick at least one role.";
  if (kinds.filter((k) => !besideWork.has(k)).length > 1)
    return "Pick one of implementer, reviewer and QA, plus planner and PM if you like.";
  return "";
}

/** "Implementer · Claude opus": what a member is and what it runs on. */
export const memberSummary = (m: Member) =>
  [
    kindsLabel(m.kinds),
    [engineLabel(m.engine), m.model].filter(Boolean).join(" "),
  ].join(" · ");

/** The open projects whose team has this member in a role. */
export const memberProjects = (member: Member, projects: Project[]) =>
  projects.filter(
    (p) =>
      p.status !== "completed" &&
      p.playbook?.roles.some((r) => r.member === member.id),
  );

/** A request keeps the team it started with after the project's changes. */
export const taskPlaybook = (task?: Task, project?: Project) =>
  task?.playbook ?? project?.playbook;

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
 * The member at work on a request now: the planner while it plans, the
 * implementer while it writes, the checker named while it is checked, and no
 * one otherwise.
 */
export function atWork(task: Task, members: Member[]) {
  if (task.status === "writing")
    return memberOf(
      task.roles?.find((r) => holds(r, "implementer")),
      members,
    );
  if (task.status === "planning")
    return memberOf(
      task.roles?.find((r) => r.name === task.checking) ??
        task.roles?.find((r) => holds(r, "planner")),
      members,
    );
  if (task.status === "reviewing" || task.status === "deciding")
    return memberOf(
      task.roles?.find((r) => r.name === task.checking),
      members,
    );
  return undefined;
}

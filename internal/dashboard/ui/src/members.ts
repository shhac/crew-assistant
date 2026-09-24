import type { Learning, Member, MemberKind, Project, Role, Task } from "./api";

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

/**
 * A learning's heading is when it applies. One without that is headed by
 * its first sentence, and the rest follows, so nothing is said twice.
 */
export function learningParts(learning: Learning) {
  const text = learning.text.trim();
  if (learning.when) return { heading: learning.when, body: text };
  const first = /^[\s\S]*?[.!?](?=\s|$)/.exec(text)?.[0] ?? text;
  return { heading: first, body: text.slice(first.length).trim() };
}

/** Who recorded a learning, and where. */
export function learnedBy(
  learning: Learning,
  member: string,
  assistant: string,
  project?: string,
) {
  const who =
    learning.source === "member"
      ? `${member} learned this`
      : learning.source === "assistant"
        ? `Added by ${assistant}`
        : "You added this";
  return project ? `${who} on ${project}` : who;
}

/** The member behind the role of a request's team with this name. */
export function roleMember(
  roles: Role[] | undefined,
  name: string | undefined,
  members: Member[],
) {
  const id = roles?.find((r) => r.name === name)?.member;
  return id ? members.find((m) => m.id === id) : undefined;
}

/**
 * The member at work on a request now: the implementer while it writes,
 * the checker named while it is checked, and no one otherwise.
 */
export function atWork(task: Task, members: Member[]) {
  if (task.status === "writing")
    return roleMember(
      task.roles,
      task.roles?.find((r) => r.kind === "implementer")?.name,
      members,
    );
  if (task.status === "reviewing" || task.status === "deciding")
    return roleMember(task.roles, task.checking, members);
  return undefined;
}

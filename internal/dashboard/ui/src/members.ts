import type {
  Member,
  MemberKind,
  Playbook,
  Project,
  Role,
  Task,
  TeamInput,
} from "./api";

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
  { id: "researcher", label: "Researcher", word: "researcher" },
  { id: "designer", label: "Designer", word: "designer" },
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
const besideWork = new Set(["researcher", "designer", "pm"]);

/**
 * What a seat does once the work has started; "" for one that only
 * researches, designs or keeps the list.
 */
export const workingKind = (role: Role) =>
  role.kinds.find((k) => !besideWork.has(k)) ?? "";

/** "Researcher and implementer": the kinds held, in the order a team works. */
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
 * research, design and keeping the list sit alongside.
 */
export function kindsProblem(kinds: readonly string[]) {
  if (!kinds.length) return "Pick at least one role.";
  if (kinds.filter((k) => !besideWork.has(k)).length > 1)
    return "Pick one of implementer, reviewer and QA, plus researcher, designer and PM if you like.";
  return "";
}

/** The seat that keeps a project's to-do list in order, if it has one. */
export const pmSeat = (project: Project) =>
  project.playbook?.roles.find((r) => holds(r, "pm"));

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

/** The seat on a team that holds a kind of role, if one does. */
export const seatFor = (playbook: Playbook, kind: string) =>
  playbook.roles.find((r) => holds(r, kind));

/**
 * The member filling a team's role of this kind, while it is still there.
 * The team keeps its copy, so a member whose kinds changed since still
 * fills the role until the owner changes it.
 */
export const seatMember = (
  playbook: Playbook,
  kind: MemberKind,
  members: Member[],
) => memberOf(seatFor(playbook, kind), members);

/** Sent as the researcher to leave research out of a code team. */
export const noResearch = "none";

/** What the owner edits of a team as a whole, apart from who fills its roles. */
export type TeamEdits = Pick<
  TeamInput,
  "template" | "writer_engine" | "reviewer_engine" | "max_rounds" | "check"
> & { deliver_to: string };

/**
 * A team to save, keeping who fills each role and where a code team works
 * from kept. The kind of work decides the shape: a code team has QA,
 * research, a workspace and a check; a writing team has none of those, only
 * a folder approved drafts are copied into.
 */
export function teamWith(
  kept: Partial<TeamInput>,
  edits: TeamEdits,
): TeamInput {
  const code = edits.template === "code";
  return {
    template: edits.template,
    writer_engine: edits.writer_engine,
    reviewer_engine: edits.reviewer_engine,
    implementer_member: kept.implementer_member ?? "",
    reviewer_member: kept.reviewer_member ?? "",
    qa_member: code ? (kept.qa_member ?? "") : "",
    researcher_member: code ? (kept.researcher_member ?? "") : "",
    designer_member: kept.designer_member ?? "",
    pm_member: kept.pm_member ?? "",
    max_rounds: edits.max_rounds,
    ...(code
      ? {
          deliver_to: "",
          repo: kept.repo ?? "",
          branch_prefix: kept.branch_prefix ?? "",
          check: edits.check ?? "",
          prepare: kept.prepare ?? [],
          sign: kept.sign ?? "",
        }
      : { deliver_to: edits.deliver_to }),
  };
}

/**
 * A project's team as it stands, as a choice to save again. A team is saved
 * whole, so a change to one part sends the rest as it is. A role a member
 * fills sends no engine, so emptying it brings back the template's.
 */
export function teamChoice(playbook: Playbook, members: Member[]): TeamInput {
  const engine = (kind: MemberKind) => {
    const role = seatFor(playbook, kind);
    return role && !role.member ? role.engine : "";
  };
  const who = (kind: MemberKind) =>
    seatMember(playbook, kind, members)?.id ?? "";
  return teamWith(
    {
      implementer_member: who("implementer"),
      reviewer_member: who("reviewer"),
      qa_member: who("qa"),
      researcher_member: seatFor(playbook, "researcher")
        ? who("researcher")
        : noResearch,
      designer_member: who("designer"),
      pm_member: who("pm"),
      repo: playbook.repo,
      branch_prefix: playbook.branch_prefix,
      prepare: playbook.prepare,
      sign: playbook.sign,
    },
    {
      template: playbook.template,
      writer_engine: engine("implementer"),
      reviewer_engine: engine("reviewer"),
      max_rounds: String(playbook.max_rounds),
      check: playbook.check,
      deliver_to: playbook.deliver_to ?? "",
    },
  );
}

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
 * The seat at work on a request now, or next to pick it up: the researcher
 * while it researches, the designer while it gives design input, the
 * implementer while it writes, the checker named while it is checked, and no
 * one otherwise.
 */
export function roleAtWork(task: Task): Role | undefined {
  if (task.status === "writing")
    return task.roles?.find((r) => holds(r, "implementer"));
  if (task.status === "researching" || task.status === "designing") {
    const kind = task.status === "researching" ? "researcher" : "designer";
    return (
      task.roles?.find((r) => r.name === task.checking) ??
      task.roles?.find((r) => holds(r, kind))
    );
  }
  if (task.status === "reviewing" || task.status === "deciding")
    return task.roles?.find((r) => r.name === task.checking);
  return undefined;
}

/** The member at work on a request now, if a member fills that seat. */
export const atWork = (task: Task, members: Member[]) =>
  memberOf(roleAtWork(task), members);

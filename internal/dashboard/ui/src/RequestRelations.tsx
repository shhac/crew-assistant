import { RequestBlockers } from "./RequestBlockers";
import { useState, type FormEvent } from "react";
import { requestHref } from "./router";
import { kindWord } from "./members";
import { projectTasks } from "./stages";
import { ErrorNotice, TaskRef, useAction } from "./ui";
import {
  linkTasks,
  unlinkTasks,
  type Member,
  type Project,
  type Relation,
  type Task,
} from "./api";

const relations: { relation: Relation; label: string }[] = [
  { relation: "depends_on", label: "Depends on" },
  { relation: "blocks", label: "Blocks" },
  { relation: "relates_to", label: "Relates to" },
  { relation: "stacks_on", label: "Stacked on" },
];

const linkedIds: Record<Relation, (task: Task) => string[] | undefined> = {
  depends_on: (task) => task.depends_on,
  blocks: (task) => task.blocks,
  relates_to: (task) => task.relates_to,
  stacks_on: (task) => (task.stacks_on ? [task.stacks_on] : undefined),
};

/** The relations the owner can add: stacking only where it is on. */
const addable = (project: Project) =>
  relations.filter(
    (r) => r.relation !== "stacks_on" || !!project.playbook?.land?.stack,
  );

const linked = (task: Task, relation: Relation) =>
  linkedIds[relation](task) ?? [];

const isRelation = (value: string): value is Relation =>
  Object.hasOwn(linkedIds, value);

/**
 * Who set a link. A blocking link is kept on the task it holds back, and a
 * link with no mark was set by the team before links were marked.
 */
function setBy(
  task: Task,
  relation: Relation,
  other: Pick<Task, "id" | "linked_by"> | undefined,
  members: Member[],
) {
  const mark =
    relation === "blocks"
      ? other?.linked_by?.[`depends_on:${task.id}`]
      : task.linked_by?.[`${relation}:${other?.id}`];
  const by = mark?.by ?? "";
  const [kind, id] = by.split(":");
  switch (kind) {
    case "owner":
      return "Set by you";
    case "assistant":
      return "Set by the assistant";
    case "pm":
      return "Set by the PM";
    case "member":
      return `Set by ${members.find((m) => m.id === id)?.name ?? "a team member"}`;
    case "role":
      return `Set by the ${kindWord(id)}`;
  }
  return "Set by the team";
}

/**
 * What this request waits for, holds back or goes with. The owner can link
 * it to any other request of the project, finished or not, and unlink any
 * link, whoever set it.
 */
export function RequestRelations({
  project,
  projects,
  task,
  tasks,
  members,
  refresh,
}: {
  project: Project;
  projects: Project[];
  task: Task;
  tasks: Task[];
  members: Member[];
  refresh: () => Promise<void>;
}) {
  const { busy, error, run } = useAction();
  const others = projectTasks(project, tasks).filter((t) => t.id !== task.id);
  const byId = new Map(tasks.map((t) => [t.id, t]));
  const groups = relations
    .map((r) => ({ ...r, ids: linked(task, r.relation) }))
    .filter((g) => g.ids.length > 0);
  const taken = new Set(groups.flatMap((g) => g.ids));
  const candidates = others.filter((t) => !taken.has(t.id));
  const unlink = (other: string) =>
    run(async () => {
      await unlinkTasks(project.id, task.id, other);
      await refresh();
    });
  const link = (relation: Relation, other: string) =>
    run(async () => {
      await linkTasks(project.id, task.id, relation, other);
      await refresh();
    });
  return (
    <section className="section" aria-label="Relations">
      <h3>Relations</h3>
      {groups.length === 0 && (
        <p className="muted small">Not linked to any other request.</p>
      )}
      {groups.map((g) => (
        <div key={g.relation} className="plan-part">
          <p className="label">{g.label}</p>
          <ul className="relations" aria-label={g.label}>
            {g.ids.map((id) => {
              const wait = task.waiting_on?.find((w) => w.task === id);
              const other =
                byId.get(id) ??
                (wait
                  ? {
                      id: wait.task,
                      project_id: wait.project_id,
                      ref: wait.ref,
                      objective: wait.objective,
                    }
                  : undefined);
              const name = other?.objective ?? "A request no longer here";
              const otherProject =
                other && other.project_id !== project.id
                  ? (projects.find((p) => p.id === other.project_id)?.title ??
                    wait?.project)
                  : undefined;
              return (
                <li key={id} className="relation">
                  <TaskRef task={other} />
                  {other ? (
                    <a href={requestHref(other.project_id, id)}>{name}</a>
                  ) : (
                    <span className="muted">{name}</span>
                  )}
                  {otherProject && (
                    <span className="muted small">in {otherProject}</span>
                  )}
                  <span className="muted small">
                    {setBy(task, g.relation, other, members)}
                  </span>
                  <button
                    type="button"
                    className="btn btn-quiet btn-sm"
                    disabled={busy}
                    aria-label={`Remove “${name}” from ${g.label}`}
                    onClick={() => void unlink(id)}
                  >
                    Remove
                  </button>
                </li>
              );
            })}
          </ul>
        </div>
      ))}
      {candidates.length > 0 && (
        <AddRelation
          candidates={candidates}
          relations={addable(project)}
          busy={busy}
          onAdd={link}
        />
      )}
      <RequestBlockers
        project={project}
        task={task}
        tasks={others}
        members={members}
        refresh={refresh}
      />
      <ErrorNotice error={error} />
    </section>
  );
}

function AddRelation({
  candidates,
  relations,
  busy,
  onAdd,
}: {
  candidates: Task[];
  relations: { relation: Relation; label: string }[];
  busy: boolean;
  onAdd: (relation: Relation, other: string) => Promise<void>;
}) {
  const [relation, setRelation] = useState<Relation>("depends_on");
  const [other, setOther] = useState("");
  // Once linked, by the owner or anyone else, a request leaves the list and
  // the choice clears; a refused one stays chosen to try another relation.
  const chosen = candidates.some((t) => t.id === other) ? other : "";
  async function add(e: FormEvent) {
    e.preventDefault();
    await onAdd(relation, chosen);
  }
  return (
    <form className="relation-add" aria-label="Add a relation" onSubmit={add}>
      <select
        className="field relation-kind"
        aria-label="Relation"
        value={relation}
        onChange={(e) => {
          if (isRelation(e.target.value)) setRelation(e.target.value);
        }}
      >
        {relations.map((r) => (
          <option key={r.relation} value={r.relation}>
            {r.label}
          </option>
        ))}
      </select>
      <select
        className="field"
        aria-label="Other request"
        value={chosen}
        onChange={(e) => setOther(e.target.value)}
      >
        <option value="">Choose a request</option>
        {candidates.map((t) => (
          <option key={t.id} value={t.id}>
            {t.objective}
          </option>
        ))}
      </select>
      <button className="btn btn-sm" type="submit" disabled={busy || !chosen}>
        Add
      </button>
    </form>
  );
}

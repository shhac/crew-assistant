import { useState, type FormEvent } from "react";
import { requestHref } from "./router";
import { kindWord } from "./members";
import { projectTasks } from "./stages";
import { ErrorNotice, useAction } from "./ui";
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
];

const linked = (task: Task, relation: Relation) =>
  (relation === "depends_on"
    ? task.depends_on
    : relation === "blocks"
      ? task.blocks
      : task.relates_to) ?? [];

/**
 * Who set a link. A blocking link is kept on the task it holds back, and a
 * link with no mark was set by the team before links were marked.
 */
function setBy(
  task: Task,
  relation: Relation,
  other: Task | undefined,
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
  task,
  tasks,
  members,
  refresh,
}: {
  project: Project;
  task: Task;
  tasks: Task[];
  members: Member[];
  refresh: () => Promise<void>;
}) {
  const { busy, error, run } = useAction();
  const others = projectTasks(project, tasks).filter((t) => t.id !== task.id);
  const byId = new Map(others.map((t) => [t.id, t]));
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
              const other = byId.get(id);
              const name = other?.objective ?? "A request no longer here";
              return (
                <li key={id} className="relation">
                  {other ? (
                    <a href={requestHref(project.id, id)}>{name}</a>
                  ) : (
                    <span className="muted">{name}</span>
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
        <AddRelation candidates={candidates} busy={busy} onAdd={link} />
      )}
      <ErrorNotice error={error} />
    </section>
  );
}

function AddRelation({
  candidates,
  busy,
  onAdd,
}: {
  candidates: Task[];
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
        onChange={(e) =>
          setRelation(
            relations.find((r) => r.relation === e.target.value)?.relation ??
              "depends_on",
          )
        }
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

import { useState, type FormEvent } from "react";
import {
  clearBlocker,
  setBlocker,
  type Blocker,
  type Member,
  type Project,
  type Task,
} from "./api";
import { ErrorNotice, useAction } from "./ui";
import { kindWord } from "./members";

function who(by: string, members: Member[]) {
  const [kind, id] = by.split(":");
  if (kind === "owner") return "you";
  if (kind === "assistant") return "the assistant";
  if (kind === "pm") return "the PM";
  if (kind === "daemon") return "the daemon";
  if (kind === "member")
    return members.find((m) => m.id === id)?.name ?? "a team member";
  if (kind === "role") return `the ${kindWord(id)}`;
  return "the team";
}

export function RequestBlockers({
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
  const [kind, setKind] = useState<Blocker["kind"]>("manual");
  const [description, setDescription] = useState("");
  const [other, setOther] = useState("");
  const [landingOnly, setLandingOnly] = useState(false);
  const finished = ["landed", "delivered", "stopped"].includes(task.status);
  async function add(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await setBlocker(project.id, task.id, {
        kind,
        description,
        task: kind === "daemon_includes" ? other : "",
        landing_only: landingOnly,
      });
      setDescription("");
      setOther("");
      await refresh();
    });
  }
  return (
    <div className="plan-part" aria-label="Waits on">
      <h4>Waits on</h4>
      {(task.blockers ?? []).map((b) => (
        <div key={b.id} className="relation">
          <span className={b.cleared_at || finished ? "muted small" : ""}>
            {b.description}
            {b.landing_only ? " (landing only)" : ""}
          </span>
          <span className="muted small">
            Set by {who(b.by, members)}
            {b.cleared_at
              ? ` · Cleared by ${who(b.cleared_by ?? "", members)}`
              : ""}
          </span>
          {!b.cleared_at && !finished && (
            <>
              {b.check && <span className="muted small">{b.check}</span>}
              <button
                type="button"
                className="btn btn-quiet btn-sm"
                disabled={busy}
                aria-label={`Clear “${b.description}”`}
                onClick={() =>
                  void run(async () => {
                    await clearBlocker(project.id, task.id, b.id);
                    await refresh();
                  })
                }
              >
                Clear
              </button>
            </>
          )}
        </div>
      ))}
      {!finished && (
        <form
          className="relation-add"
          aria-label="Add an external blocker"
          onSubmit={add}
        >
          <select
            className="field"
            aria-label="Condition kind"
            value={kind}
            onChange={(e) =>
              setKind(
                e.target.value === "daemon_includes"
                  ? "daemon_includes"
                  : "manual",
              )
            }
          >
            <option value="manual">Manual condition</option>
            {project.playbook?.medium === "git" && (
              <option value="daemon_includes">Daemon includes request</option>
            )}
          </select>
          <input
            className="field"
            aria-label="Condition description"
            placeholder="Wait until…"
            maxLength={300}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
          {kind === "daemon_includes" && (
            <select
              className="field"
              aria-label="Included request"
              value={other}
              onChange={(e) => setOther(e.target.value)}
            >
              <option value="">Choose a request</option>
              {tasks
                .filter((t) => t.id !== task.id)
                .map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.ref ?? t.objective}: {t.objective}
                  </option>
                ))}
            </select>
          )}
          <select
            className="field"
            aria-label="Hold until cleared"
            value={landingOnly ? "landing" : "start"}
            onChange={(e) => setLandingOnly(e.target.value === "landing")}
          >
            <option value="start">Hold start and landing</option>
            <option value="landing">Hold landing only</option>
          </select>
          <button
            type="submit"
            className="btn btn-sm"
            disabled={
              busy || (kind === "manual" ? !description.trim() : !other)
            }
          >
            Add condition
          </button>
        </form>
      )}
      <ErrorNotice error={error} />
    </div>
  );
}

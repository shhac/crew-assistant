import { useState, type FormEvent } from "react";
import { projectHref } from "./router";
import { ErrorNotice, TaskRef, useAction } from "./ui";
import { askForTask, criteriaLines, type Project, type Task } from "./api";

import { finished, projectTasks } from "./stages";

export function AskForm({
  project,
  refresh,
  tasks,
  projects,
}: {
  project: Project;
  tasks: Task[];
  projects: Project[];
  refresh: () => Promise<void>;
}) {
  const [objective, setObjective] = useState("");
  const [criteria, setCriteria] = useState("");
  const [ownerChecks, setOwnerChecks] = useState("");
  const [checking, setChecking] = useState(false);
  const [judging, setJudging] = useState(false);
  const [waiting, setWaiting] = useState(false);
  const [dependsOn, setDependsOn] = useState<string[]>([]);
  const { busy, error, run } = useAction();
  if (!project.brief.goal)
    return (
      <p className="ask-blocked card">
        Needs a brief first.{" "}
        <a href={projectHref(project.id, "brief")}>Write the brief</a>
      </p>
    );
  if (!project.playbook)
    return (
      <p className="ask-blocked card">
        Needs a team first.{" "}
        <a href={projectHref(project.id, "config")}>Choose a team</a>
      </p>
    );
  async function ask(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await askForTask(project.id, {
        objective: objective.trim(),
        criteria: criteriaLines(criteria),
        owner_checks: criteriaLines(ownerChecks),
        ...(dependsOn.length ? { depends_on: dependsOn } : {}),
      });
      setDependsOn([]);
      setWaiting(false);
      setObjective("");
      setCriteria("");
      setOwnerChecks("");
      setChecking(false);
      setJudging(false);
      await refresh();
    });
  }
  return (
    <form className="ask card" onSubmit={ask} aria-label="Ask the team">
      <div className="ask-row">
        <label className="sr-only" htmlFor="ask-objective">
          What do you want?
        </label>
        <input
          id="ask-objective"
          className="field"
          value={objective}
          onChange={(e) => setObjective(e.target.value)}
          placeholder="Ask the team for something"
          maxLength={20000}
          required
        />
        <button
          className="btn btn-primary"
          type="submit"
          disabled={busy || !objective.trim()}
        >
          Ask
        </button>
      </div>
      {judging ? (
        <label className="control">
          How you'll judge it, one point per line
          <textarea
            className="field"
            value={criteria}
            onChange={(e) => setCriteria(e.target.value)}
            rows={2}
            maxLength={20000}
          />
        </label>
      ) : (
        <button
          type="button"
          className="link-button small ask-more"
          onClick={() => setJudging(true)}
        >
          + Say how you'll judge it
        </button>
      )}
      {checking ? (
        <label className="control">
          What you'll check after it lands, one point per line
          <textarea
            className="field"
            value={ownerChecks}
            onChange={(e) => setOwnerChecks(e.target.value)}
            rows={2}
            maxLength={20000}
          />
        </label>
      ) : (
        <button
          type="button"
          className="link-button small ask-more"
          onClick={() => setChecking(true)}
        >
          + Say what you'll check after it lands
        </button>
      )}
      {waiting ? (
        <div className="relations">
          <label className="relation-add">
            What it waits for
            <select
              className="field"
              value=""
              onChange={(e) => {
                if (e.target.value)
                  setDependsOn([...dependsOn, e.target.value]);
              }}
            >
              <option value="">Choose a task</option>
              {[project, ...projects.filter((p) => p.id !== project.id)].map(
                (p) => (
                  <optgroup key={p.id} label={p.title}>
                    {projectTasks(p, tasks)
                      .filter((t) => !finished(t) && !dependsOn.includes(t.id))
                      .map((t) => (
                        <option key={t.id} value={t.id}>
                          {t.ref} {t.objective}
                        </option>
                      ))}
                  </optgroup>
                ),
              )}
            </select>
          </label>
          {dependsOn.map((id) => {
            const task = tasks.find((t) => t.id === id);
            if (!task) return null;
            const other = projects.find((p) => p.id === task.project_id);
            return (
              <div className="relation" key={id}>
                <TaskRef task={task} /> {task.objective}
                {task.project_id !== project.id && other && (
                  <span> in {other.title}</span>
                )}
                <button
                  type="button"
                  className="link-button small"
                  aria-label={`Remove “${task.objective}” from waits for`}
                  onClick={() =>
                    setDependsOn(dependsOn.filter((dep) => dep !== id))
                  }
                >
                  Remove
                </button>
              </div>
            );
          })}
        </div>
      ) : (
        <button
          type="button"
          className="link-button small ask-more"
          onClick={() => setWaiting(true)}
        >
          + Say what it waits for
        </button>
      )}
      <ErrorNotice error={error} />
    </form>
  );
}

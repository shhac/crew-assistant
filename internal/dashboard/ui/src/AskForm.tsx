import { useState, type FormEvent } from "react";
import { projectHref } from "./router";
import { ErrorNotice, useAction } from "./ui";
import { askForTask, criteriaLines, type Project } from "./api";

export function AskForm({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  const [objective, setObjective] = useState("");
  const [criteria, setCriteria] = useState("");
  const [judging, setJudging] = useState(false);
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
      });
      setObjective("");
      setCriteria("");
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
      <ErrorNotice error={error} />
    </form>
  );
}

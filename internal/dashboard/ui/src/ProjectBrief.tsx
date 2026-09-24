import { useState, type FormEvent } from "react";
import { BriefFields } from "./ProjectForms";
import { CriteriaList, dateLabel, ErrorNotice, useAction } from "./ui";
import { criteriaLines, updateBrief, type Brief, type Project } from "./api";

export function BriefTab({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const brief = project.brief;
  if (editing)
    return (
      <section className="tab-panel card">
        <BriefEditor
          project={project}
          onDone={() => setEditing(false)}
          refresh={refresh}
        />
      </section>
    );
  return (
    <section className="tab-panel card">
      <div className="panel-head">
        <h2>Brief</h2>
        <button
          type="button"
          className="btn btn-sm"
          onClick={() => setEditing(true)}
        >
          {brief.goal ? "Edit" : "Write the brief"}
        </button>
      </div>
      <BriefView brief={brief} />
    </section>
  );
}

function BriefView({ brief }: { brief: Brief }) {
  if (!brief.goal)
    return (
      <p className="muted">
        No brief yet. The team checks its work against it.
      </p>
    );
  return (
    <dl className="facts">
      <dt>Goal</dt>
      <dd>{brief.goal}</dd>
      {brief.audience && (
        <>
          <dt>For</dt>
          <dd>{brief.audience}</dd>
        </>
      )}
      {brief.constraints && (
        <>
          <dt>Constraints</dt>
          <dd>{brief.constraints}</dd>
        </>
      )}
      <dt>Done when</dt>
      <dd>
        <CriteriaList
          criteria={brief.criteria}
          empty="Nothing listed."
          marker
        />
      </dd>
      <dt className="sr-only">Version</dt>
      <dd className="muted small">
        Version {brief.version}
        {dateLabel(brief.updated_at) && ` · ${dateLabel(brief.updated_at)}`}
      </dd>
    </dl>
  );
}

function BriefEditor({
  project,
  onDone,
  refresh,
}: {
  project: Project;
  onDone: () => void;
  refresh: () => Promise<void>;
}) {
  const brief = project.brief;
  const [goal, setGoal] = useState(brief.goal);
  const [audience, setAudience] = useState(brief.audience ?? "");
  const [constraints, setConstraints] = useState(brief.constraints ?? "");
  const [criteria, setCriteria] = useState(
    criteriaLines(brief.criteria).join("\n"),
  );
  const { busy, error, run } = useAction();
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await updateBrief(project.id, {
        goal: goal.trim(),
        audience: audience.trim(),
        constraints: constraints.trim(),
        criteria: criteriaLines(criteria),
      });
      await refresh();
      onDone();
    });
  }
  return (
    <form className="form" onSubmit={save} aria-label="Brief">
      <label htmlFor="brief-goal">
        Goal
        <textarea
          id="brief-goal"
          className="field"
          value={goal}
          onChange={(e) => setGoal(e.target.value)}
          placeholder="What is this project for?"
          rows={2}
          maxLength={20000}
          required
        />
      </label>
      <BriefFields
        idPrefix="brief"
        audience={audience}
        constraints={constraints}
        criteria={criteria}
        onAudience={setAudience}
        onConstraints={setConstraints}
        onCriteria={setCriteria}
      />
      {brief.version > 0 && (
        <p className="hint">
          Saving makes version {brief.version + 1}. Work under way is checked
          against it.
        </p>
      )}
      <ErrorNotice error={error} />
      <div className="actions">
        <button
          className="btn btn-primary"
          type="submit"
          disabled={busy || !goal.trim()}
        >
          Save brief
        </button>
        <button
          className="btn btn-quiet"
          type="button"
          disabled={busy}
          onClick={onDone}
        >
          Cancel
        </button>
      </div>
    </form>
  );
}

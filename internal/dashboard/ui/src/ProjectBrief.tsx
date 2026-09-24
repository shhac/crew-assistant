import { useState, type FormEvent } from "react";
import { BriefFields } from "./ProjectForms";
import { CriteriaList, dateLabel, ErrorNotice, useAction } from "./ui";
import { criteriaLines, updateBrief, type Brief, type Project } from "./api";

export function BriefCard({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const brief = project.brief;
  return (
    <section className="project-card" aria-label="Brief">
      <div className="section-heading">
        <h2>Brief</h2>
        {!editing && (
          <button
            type="button"
            className="text-button"
            onClick={() => setEditing(true)}
          >
            {brief.goal ? "Edit brief" : "Write the brief"}
          </button>
        )}
      </div>
      {editing ? (
        <BriefEditor
          project={project}
          onDone={() => setEditing(false)}
          refresh={refresh}
        />
      ) : (
        <BriefView brief={brief} />
      )}
    </section>
  );
}

function BriefView({ brief }: { brief: Brief }) {
  if (!brief.goal)
    return (
      <p className="muted">
        No brief yet. A brief says what the project is for, so the work can be
        judged against it.
      </p>
    );
  return (
    <dl className="brief-fields">
      <dt>Goal</dt>
      <dd>{brief.goal}</dd>
      {brief.audience && (
        <>
          <dt>Audience</dt>
          <dd>{brief.audience}</dd>
        </>
      )}
      {brief.constraints && (
        <>
          <dt>Constraints</dt>
          <dd>{brief.constraints}</dd>
        </>
      )}
      <dt>What done looks like</dt>
      <dd>
        <CriteriaList
          criteria={brief.criteria}
          empty="No criteria recorded."
          marker
        />
      </dd>
      <dd className="field-hint">
        Version {brief.version}
        {dateLabel(brief.updated_at) &&
          ` · updated ${dateLabel(brief.updated_at)}`}
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
    <form className="project-card-form" onSubmit={save}>
      <label htmlFor="brief-goal">
        Goal
        <textarea
          id="brief-goal"
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
        <p className="field-hint">
          Saving makes version {brief.version + 1}. Work under way is checked
          again against it.
        </p>
      )}
      <ErrorNotice error={error} />
      <div className="form-actions">
        <button
          className="button primary"
          type="submit"
          disabled={busy || !goal.trim()}
        >
          {busy ? "Saving…" : "Save brief"}
        </button>
        <button
          className="text-button"
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

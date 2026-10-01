import { useState } from "react";
import {
  approvalText,
  landingPausedLine,
  mergeMethod,
  reversibility,
  wayFor,
  wayOf,
  whatHappens,
} from "./landing";
import { LandingEditor } from "./LandingEditor";
import { ErrorNotice, useAction } from "./ui";
import { setLandingPaused, type Playbook, type Project } from "./api";

/**
 * What landing an approved change means for this project. Only the owner and
 * the assistant can change it; nothing the team does can.
 */
export function LandingSettings({
  project,
  playbook,
  refresh,
}: {
  project: Project;
  playbook: Playbook;
  refresh: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const land = playbook.land;
  if (editing)
    return (
      <section className="tab-panel card" aria-label="Landing">
        <LandingEditor
          project={project}
          onDone={() => setEditing(false)}
          refresh={refresh}
        />
      </section>
    );
  const via = wayOf(land);
  return (
    <section className="tab-panel card" aria-label="Landing">
      <div className="panel-head">
        <h3>Landing</h3>
        <button
          type="button"
          className="btn btn-sm"
          onClick={() => setEditing(true)}
        >
          Edit
        </button>
      </div>
      <dl className="facts">
        {land?.means && (
          <div className="fact-row">
            <dt>Landing means</dt>
            <dd>{land.means}</dd>
          </div>
        )}
        <div className="fact-row">
          <dt>Lands as</dt>
          <dd>
            {wayFor(via)?.label}
            {via === "push" && (
              <>
                : <code>{land?.target}</code>
              </>
            )}
            {via === "pull-request" && (
              <>
                {" "}
                on <code>{land?.github}</code> into <code>{land?.target}</code>,
                merged by {mergeMethod(land)}
              </>
            )}
          </dd>
        </div>
        <div className="fact-row">
          <dt>Before it lands</dt>
          <dd>{approvalText(land)}</dd>
        </div>
        <div className="fact-row">
          <dt>To undo</dt>
          <dd>{reversibility(land)}</dd>
        </div>
      </dl>
      <div className="section">
        <p className="label">When a change lands</p>
        <ol className="happens">
          {whatHappens(playbook).map((line) => (
            <li key={line}>{line}</li>
          ))}
        </ol>
      </div>
      <p className="muted small">
        Nothing the team does can change this, and a branch the team doesn't own
        is never overwritten.
      </p>
      <PauseLanding project={project} refresh={refresh} />
    </section>
  );
}

/**
 * Holding everything landing, as for a code freeze, with why; the rest of
 * the project's work goes on.
 */
function PauseLanding({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  const [reason, setReason] = useState("");
  const { busy, error, run } = useAction();
  const paused = project.landing_paused;
  const prs = !!project.playbook?.land?.pull_requests;
  const toggle = (pause: boolean) =>
    run(async () => {
      await setLandingPaused(project.id, pause, pause ? reason.trim() : "");
      setReason("");
      await refresh();
    });
  return (
    <div className="section">
      <p className="label">Pause landing</p>
      {paused ? (
        <>
          <p>{landingPausedLine(project)}</p>
          <button
            type="button"
            className="btn btn-sm"
            disabled={busy}
            onClick={() => void toggle(false)}
          >
            Resume landing
          </button>
        </>
      ) : (
        <form
          className="form-row"
          onSubmit={(e) => {
            e.preventDefault();
            void toggle(true);
          }}
        >
          <label htmlFor="land-pause-reason">
            Why
            <input
              id="land-pause-reason"
              className="field"
              value={reason}
              maxLength={300}
              placeholder="Release freeze until Friday"
              onChange={(e) => setReason(e.target.value)}
            />
          </label>
          <button type="submit" className="btn btn-sm" disabled={busy}>
            Pause landing
          </button>
        </form>
      )}
      <p className="hint">
        {prs
          ? "Pull requests still open and the team still answers their reviews; nothing merges until you resume."
          : "Nothing lands until you resume; the team's other work goes on."}
      </p>
      <ErrorNotice error={error} />
    </div>
  );
}

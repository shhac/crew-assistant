import { useState, type FormEvent } from "react";
import {
  approvalText,
  landingWays,
  mergeMethod,
  pmCanDecide,
  reversibility,
  wayFor,
  whatHappens,
} from "./landing";
import { ErrorNotice, useAction } from "./ui";
import { setLanding, type Playbook, type Project } from "./api";

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
  const via = land?.via || "branch";
  return (
    <section className="tab-panel card" aria-label="Landing">
      <div className="panel-head">
        <h2>Landing</h2>
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
    </section>
  );
}

function LandingEditor({
  project,
  onDone,
  refresh,
}: {
  project: Project;
  onDone: () => void;
  refresh: () => Promise<void>;
}) {
  const land = project.playbook?.land;
  const [via, setVia] = useState(land?.via || "branch");
  const [github, setGithub] = useState(land?.github ?? "");
  const [method, setMethod] = useState(
    land?.via === "pull-request" ? mergeMethod(land) : "squash",
  );
  const [target, setTarget] = useState(land?.target || "main");
  const [chosenApprove, setApprove] = useState(land?.approve || "before");
  // Only a push can leave landing to the PM; any other way asks the owner.
  const approve =
    chosenApprove === "pm" && !pmCanDecide(via) ? "before" : chosenApprove;
  const hasPM = !!project.playbook?.roles.some((r) => r.kinds.includes("pm"));
  const approveHint = !pmCanDecide(via)
    ? via === "pull-request"
      ? "The PM can't decide here: GitHub's reviews decide when a pull request merges."
      : "The PM can't decide here: a new branch lands nothing."
    : approve !== "pm"
      ? ""
      : hasPM
        ? "Once the reviewers and QA pass a change, nothing waits on you and what it depends on has landed, the PM lands or holds it and says why. It lands a change as one commit or keeps the team's commits, and cleans up the branch. You can still land or stop it yourself."
        : "The team has no PM yet, so you're asked until it has one.";
  const [means, setMeans] = useState(land?.means ?? "");
  const { busy, error, run } = useAction();
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await setLanding(project.id, {
        means: means.trim(),
        via,
        target: via === "branch" ? "" : target.trim(),
        method: wayFor(via)?.method(method) ?? "",
        github: via === "pull-request" ? github.trim() : "",
        approve,
      });
      await refresh();
      onDone();
    });
  }
  return (
    <form className="form" aria-label="Landing" onSubmit={save}>
      <h2>Landing</h2>
      <div className="form-row">
        <label htmlFor="land-via">
          Lands as
          <select
            id="land-via"
            className="field"
            value={via}
            onChange={(e) => setVia(e.target.value)}
          >
            {Object.entries(landingWays).map(([id, way]) => (
              <option key={id} value={id}>
                {way.label}
              </option>
            ))}
          </select>
        </label>
        {via === "pull-request" && (
          <label htmlFor="land-github">
            GitHub repository
            <input
              id="land-github"
              className="field"
              value={github}
              placeholder="owner/name"
              onChange={(e) => setGithub(e.target.value)}
              required
            />
          </label>
        )}
        {via === "pull-request" && (
          <label htmlFor="land-method">
            Merge by
            <select
              id="land-method"
              className="field"
              value={method}
              onChange={(e) => setMethod(e.target.value)}
            >
              <option value="squash">Squash</option>
              <option value="merge">Merge commit</option>
              <option value="rebase">Rebase</option>
            </select>
          </label>
        )}
        {via !== "branch" && (
          <label htmlFor="land-target">
            {via === "pull-request" ? "Into branch" : "Branch"}
            <input
              id="land-target"
              className="field"
              value={target}
              onChange={(e) => setTarget(e.target.value)}
              required
            />
          </label>
        )}
        <label htmlFor="land-approve">
          Before it lands
          <select
            id="land-approve"
            className="field"
            value={approve}
            onChange={(e) => setApprove(e.target.value)}
          >
            <option value="before">Ask me first</option>
            <option value="none">Land once the checks pass</option>
            {pmCanDecide(via) && <option value="pm">The PM decides</option>}
          </select>
          {approveHint && <span className="hint">{approveHint}</span>}
        </label>
      </div>
      <label htmlFor="land-means">
        What landing means here
        <input
          id="land-means"
          className="field"
          value={means}
          placeholder="fast-forwarded onto main"
          onChange={(e) => setMeans(e.target.value)}
        />
        <span className="hint">Optional. The team reads it.</span>
      </label>
      <ErrorNotice error={error} />
      <div className="actions">
        <button className="btn btn-primary" type="submit" disabled={busy}>
          Save
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

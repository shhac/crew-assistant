import { useState, type FormEvent } from "react";
import {
  approvalText,
  landingPausedLine,
  landingWays,
  openGate,
  mergeGate,
  mergeMethod,
  pmCanDecide,
  reversibility,
  wayFor,
  wayOf,
  whatHappens,
} from "./landing";
import { ErrorNotice, useAction } from "./ui";
import {
  setLanding,
  setLandingPaused,
  type Playbook,
  type Project,
} from "./api";

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
  const [pullRequests, setPullRequests] = useState(!!land?.pull_requests);
  const [github, setGithub] = useState(land?.github ?? "");
  const [merge, setMerge] = useState(mergeMethod(land));
  const [open, setOpen] = useState(openGate(land));
  const [merging, setMerging] = useState(mergeGate(land));
  const [target, setTarget] = useState(land?.target || "main");
  const [chosenApprove, setApprove] = useState(land?.approve || "before");
  const way = pullRequests ? "pull-request" : via;
  // Only a push can leave landing to the PM; any other way asks the owner.
  const approve =
    chosenApprove === "pm" && !pmCanDecide(way) ? "before" : chosenApprove;
  const hasPM = !!project.playbook?.roles.some((r) => r.kinds.includes("pm"));
  const approveHint = !pmCanDecide(way)
    ? "The PM can't decide here: a new branch lands nothing."
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
        target: way === "branch" ? "" : target.trim(),
        method: via === "push" ? "fast-forward" : "",
        pull_requests: pullRequests,
        github: pullRequests ? github.trim() : "",
        merge: pullRequests ? merge : "",
        open: pullRequests ? open : "",
        approve: pullRequests ? merging : approve,
      });
      await refresh();
      onDone();
    });
  }
  return (
    <form className="form" aria-label="Landing" onSubmit={save}>
      <h3>Landing</h3>
      <label className="check">
        <input
          type="checkbox"
          checked={pullRequests}
          onChange={(e) => setPullRequests(e.target.checked)}
        />
        <span>Use pull requests</span>
      </label>
      <div className="form-row">
        <label htmlFor="land-via">
          {pullRequests ? "Without pull requests, lands as" : "Lands as"}
          <select
            id="land-via"
            className="field"
            value={via}
            onChange={(e) => setVia(e.target.value)}
          >
            {(["branch", "push"] as const).map((id) => (
              <option key={id} value={id}>
                {landingWays[id].label}
              </option>
            ))}
          </select>
        </label>
        {pullRequests && (
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
        {pullRequests && (
          <label htmlFor="land-method">
            Merge by
            <select
              id="land-method"
              className="field"
              value={merge}
              onChange={(e) => setMerge(e.target.value)}
            >
              <option value="squash">Squash</option>
              <option value="merge">Merge commit</option>
              <option value="rebase">Rebase</option>
            </select>
          </label>
        )}
        {way !== "branch" && (
          <label htmlFor="land-target">
            {pullRequests ? "Into branch" : "Branch"}
            <input
              id="land-target"
              className="field"
              value={target}
              onChange={(e) => setTarget(e.target.value)}
              required
            />
          </label>
        )}
        {pullRequests && (
          <label htmlFor="land-open">
            Opening a pull request
            <select
              id="land-open"
              className="field"
              value={open}
              onChange={(e) => setOpen(e.target.value)}
            >
              <option value="pm">The PM decides</option>
              <option value="owner">Ask me first</option>
              <option value="implementer">The implementer decides</option>
            </select>
            <span className="hint">
              {open === "pm"
                ? hasPM
                  ? "Once the reviewers and QA pass a change, the PM opens its pull request or holds it and says why. You can still open it yourself."
                  : "The team has no PM yet, so you're asked until it has one."
                : open === "owner"
                  ? "You see the title and description the implementer wrote before it opens."
                  : "A change opens its pull request as soon as the reviewers and QA pass it."}
            </span>
          </label>
        )}
        {pullRequests && (
          <label htmlFor="land-merge">
            Before it merges
            <select
              id="land-merge"
              className="field"
              value={merging}
              onChange={(e) => setMerging(e.target.value)}
            >
              <option value="pm">The PM decides</option>
              <option value="before">Ask me first</option>
              <option value="none">Merge once it's ready</option>
            </select>
            <span className="hint">
              Ready means approved where the repository asks for review, every
              check green, every review thread resolved, and no conflicts.
              {merging === "pm" &&
                !hasPM &&
                " The team has no PM yet, so you're asked until it has one."}
            </span>
          </label>
        )}
        {!pullRequests && (
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
              {pmCanDecide(way) && <option value="pm">The PM decides</option>}
            </select>
            {approveHint && <span className="hint">{approveHint}</span>}
          </label>
        )}
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

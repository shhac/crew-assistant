import { useState, type FormEvent } from "react";
import {
  approveHint,
  botsHint,
  draftHint,
  effectiveApprove,
  landingInput,
  landingWays,
  mergeGate,
  mergeHint,
  mergeMethod,
  openGate,
  openHint,
  pmCanDecide,
} from "./landing";
import { ErrorNotice, useAction } from "./ui";
import { setLanding, type Project } from "./api";

export function LandingEditor({
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
  const [draft, setDraft] = useState(!!land?.draft);
  const [bots, setBots] = useState((land?.trusted_bots ?? []).join(", "));
  const way = pullRequests ? "pull-request" : via;
  const approve = effectiveApprove(chosenApprove, way);
  const hasPM = !!project.playbook?.roles.some((r) => r.kinds.includes("pm"));
  const approving = approveHint(way, approve, hasPM);
  const [means, setMeans] = useState(land?.means ?? "");
  const { busy, error, run } = useAction();
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await setLanding(
        project.id,
        landingInput({
          means,
          via,
          pullRequests,
          target,
          github,
          merge,
          open,
          merging,
          approve: chosenApprove,
          draft,
          bots,
        }),
      );
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
      {pullRequests && (
        <>
          <label className="check">
            <input
              type="checkbox"
              checked={draft}
              onChange={(e) => setDraft(e.target.checked)}
              aria-describedby="land-draft-hint"
            />
            <span>Open them as drafts</span>
          </label>
          <p className="hint" id="land-draft-hint">
            {draftHint}
          </p>
        </>
      )}
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
            <span className="hint">{openHint(open, hasPM)}</span>
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
            <span className="hint">{mergeHint(merging, hasPM)}</span>
          </label>
        )}
        {pullRequests && (
          <label htmlFor="land-bots">
            Automated reviewers the team trusts
            <input
              id="land-bots"
              className="field"
              value={bots}
              placeholder="review-bot[bot]"
              onChange={(e) => setBots(e.target.value)}
            />
            <span className="hint">{botsHint}</span>
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
            {approving && <span className="hint">{approving}</span>}
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

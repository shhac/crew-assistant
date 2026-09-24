import { useState, type FormEvent } from "react";
import { ErrorNotice, useAction } from "./ui";
import { setLanding, type Project } from "./api";

// landingMethod is the merge method each way takes: a push only lands by
// fast-forward, a new branch needs none.
function landingMethod(via: string, chosen: string) {
  const methods: Record<string, string> = {
    push: "fast-forward",
    "pull-request": chosen,
  };
  return methods[via] ?? "";
}

function landingSummary(project: Project) {
  const land = project.playbook?.land;
  const ask =
    land?.approve === "none"
      ? "A change that passes its checks lands without asking you."
      : "You approve each change before it lands.";
  if (land?.via === "push")
    return `Approved changes land on ${land.target} by fast-forward: it only moves forward, and nothing already there is replaced. ${ask}`;
  if (land?.via === "pull-request")
    return `Approved changes open a pull request on ${land.github} into ${land.target}; the team answers its reviews and CI, and it merges by ${land.method || "squash"} once GitHub says it is approved and green. ${ask}`;
  return `Approved changes become a local branch starting ${project.playbook?.branch_prefix ?? ""}; nothing is pushed. ${ask}`;
}

export function LandingCard({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  const land = project.playbook?.land;
  const [editing, setEditing] = useState(false);
  const [via, setVia] = useState(land?.via || "branch");
  const [github, setGithub] = useState(land?.github ?? "");
  const [method, setMethod] = useState(
    land?.via === "pull-request" ? land.method || "squash" : "squash",
  );
  const [target, setTarget] = useState(land?.target || "main");
  const [approve, setApprove] = useState(land?.approve || "before");
  const [means, setMeans] = useState(land?.means ?? "");
  const { busy, error, run } = useAction();
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await setLanding(project.id, {
        means: means.trim(),
        via,
        target: via === "branch" ? "" : target.trim(),
        method: landingMethod(via, method),
        github: via === "pull-request" ? github.trim() : "",
        approve,
      });
      await refresh();
      setEditing(false);
    });
  }
  if (!editing)
    return (
      <div className="team-landing" aria-label="Landing">
        <p className="field-hint">
          {land?.means && (
            <>
              <strong>Landing means:</strong> {land.means}.{" "}
            </>
          )}
          {landingSummary(project)}
        </p>
        <button
          type="button"
          className="text-button"
          onClick={() => setEditing(true)}
        >
          Change where changes land
        </button>
      </div>
    );
  return (
    <form className="project-card-form" aria-label="Landing" onSubmit={save}>
      <div className="team-fields">
        <label htmlFor="land-via">
          When approved
          <select
            id="land-via"
            value={via}
            onChange={(e) => setVia(e.target.value)}
          >
            <option value="branch">Create a new local branch</option>
            <option value="push">Fast-forward a branch</option>
            <option value="pull-request">Open a pull request on GitHub</option>
          </select>
        </label>
        {via === "pull-request" && (
          <label htmlFor="land-github">
            GitHub repository
            <input
              id="land-github"
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
            {via === "pull-request" ? "Into branch" : "Branch to land on"}
            <input
              id="land-target"
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
            value={approve}
            onChange={(e) => setApprove(e.target.value)}
          >
            <option value="before">Ask me first</option>
            <option value="none">Land when the checks pass</option>
          </select>
        </label>
      </div>
      <label htmlFor="land-means">
        What landing means here (optional)
        <input
          id="land-means"
          value={means}
          placeholder="fully fast-forward merged to main"
          onChange={(e) => setMeans(e.target.value)}
        />
      </label>
      <ErrorNotice error={error} />
      <div className="form-actions">
        <button className="button primary" type="submit" disabled={busy}>
          {busy ? "Saving…" : "Save"}
        </button>
        <button
          className="text-button"
          type="button"
          disabled={busy}
          onClick={() => setEditing(false)}
        >
          Cancel
        </button>
      </div>
    </form>
  );
}

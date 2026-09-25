import { useState, type FormEvent } from "react";
import { Folders } from "./ProjectFolders";
import { LandingSettings } from "./ProjectLanding";
import { TeamSettings } from "./TeamSettings";
import { isCode } from "./stages";
import { ErrorNotice, useAction } from "./ui";
import { setWorkspace, type Member, type Playbook, type Project } from "./api";

const signing: Record<string, string> = {
  "": "Signed as your git config says",
  always: "Always signed",
  never: "Never signed",
};

/** How a project's team works, where its work happens and how it lands. */
export function ConfigTab({
  project,
  members,
  refresh,
}: {
  project: Project;
  members: Member[];
  refresh: () => Promise<void>;
}) {
  const playbook = project.playbook;
  const code = playbook && isCode(playbook);
  return (
    <div className="tab-stack">
      <TeamSettings project={project} members={members} refresh={refresh} />
      {code && (
        <>
          <LandingSettings
            project={project}
            playbook={playbook}
            refresh={refresh}
          />
          <Workspace project={project} playbook={playbook} refresh={refresh} />
        </>
      )}
      <Folders project={project} refresh={refresh} />
    </div>
  );
}

/** The repository a code team works on, and how its work is kept there. */
function Workspace({
  project,
  playbook,
  refresh,
}: {
  project: Project;
  playbook: Playbook;
  refresh: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  if (editing)
    return (
      <section className="tab-panel card" aria-label="Workspace">
        <WorkspaceEditor
          project={project}
          playbook={playbook}
          onDone={() => setEditing(false)}
          refresh={refresh}
        />
      </section>
    );
  return (
    <section className="tab-panel card" aria-label="Workspace">
      <div className="panel-head">
        <h2>Workspace</h2>
        <button
          type="button"
          className="btn btn-sm"
          onClick={() => setEditing(true)}
        >
          Edit
        </button>
      </div>
      <dl className="facts">
        <div className="fact-row">
          <dt>Works in</dt>
          <dd>
            A private copy of <code>{playbook.repo}</code>
          </dd>
        </div>
        <div className="fact-row">
          <dt>Branches</dt>
          <dd>
            Start with <code>{playbook.branch_prefix}</code>
          </dd>
        </div>
        <div className="fact-row">
          <dt>Copied in</dt>
          <dd>
            {playbook.prepare?.length
              ? playbook.prepare.map((p, i) => (
                  <span key={p}>
                    {i > 0 && ", "}
                    <code>{p}</code>
                  </span>
                ))
              : "No ignored folders"}
          </dd>
        </div>
        <div className="fact-row">
          <dt>Commits</dt>
          <dd>{signing[playbook.sign ?? ""]}</dd>
        </div>
      </dl>
    </section>
  );
}

function WorkspaceEditor({
  project,
  playbook,
  onDone,
  refresh,
}: {
  project: Project;
  playbook: Playbook;
  onDone: () => void;
  refresh: () => Promise<void>;
}) {
  const folders = project.directories ?? [];
  const [repo, setRepo] = useState(playbook.repo ?? folders[0] ?? "");
  const [branchPrefix, setBranchPrefix] = useState(
    playbook.branch_prefix ?? "crew/",
  );
  const [prepare, setPrepare] = useState((playbook.prepare ?? []).join(", "));
  const [sign, setSign] = useState(playbook.sign ?? "");
  const { busy, error, run } = useAction();
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await setWorkspace(project.id, {
        repo,
        branch_prefix: branchPrefix.trim(),
        prepare: prepare
          .split(/[,\n]/)
          .map((p) => p.trim())
          .filter(Boolean),
        sign,
      });
      await refresh();
      onDone();
    });
  }
  return (
    <form className="form" aria-label="Workspace" onSubmit={save}>
      <h2>Workspace</h2>
      <div className="form-row">
        <label htmlFor="config-repo">
          Repository
          <select
            id="config-repo"
            className="field"
            value={repo}
            onChange={(e) => setRepo(e.target.value)}
          >
            {folders.map((folder) => (
              <option key={folder} value={folder}>
                {folder}
              </option>
            ))}
          </select>
        </label>
        <label htmlFor="config-prefix">
          Branch prefix
          <input
            id="config-prefix"
            className="field"
            value={branchPrefix}
            onChange={(e) => setBranchPrefix(e.target.value)}
            required
          />
        </label>
        <label htmlFor="config-prepare">
          Ignored folders to copy in
          <input
            id="config-prepare"
            className="field"
            value={prepare}
            placeholder="node_modules"
            onChange={(e) => setPrepare(e.target.value)}
          />
          <span className="hint">Optional. Separate with commas.</span>
        </label>
        <label htmlFor="config-sign">
          Sign commits
          <select
            id="config-sign"
            className="field"
            value={sign}
            onChange={(e) => setSign(e.target.value)}
          >
            <option value="">As your git config says</option>
            <option value="always">Always</option>
            <option value="never">Never</option>
          </select>
        </label>
      </div>
      <p className="hint">
        Requests already under way keep the workspace they started with.
      </p>
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

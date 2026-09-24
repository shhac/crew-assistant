import { useState, type FormEvent } from "react";
import { FileSystemPicker } from "./FileSystemPicker";
import { Folders } from "./ProjectFolders";
import { engineLabel, engines, isCode } from "./stages";
import { ErrorNotice, useAction } from "./ui";
import { setTeam, type Playbook, type Project, type Role } from "./api";

const signing: Record<string, string> = {
  "": "Signed as your git config says",
  always: "Always signed",
  never: "Never signed",
};

export function TeamTab({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const playbook = project.playbook;
  return (
    <div className="tab-stack">
      <section className="tab-panel card" aria-label="Team">
        {editing ? (
          <TeamEditor
            project={project}
            onDone={() => setEditing(false)}
            refresh={refresh}
          />
        ) : (
          <>
            <div className="panel-head">
              <h2>Team</h2>
              <button
                type="button"
                className="btn btn-sm"
                onClick={() => setEditing(true)}
              >
                {playbook ? "Edit" : "Choose a team"}
              </button>
            </div>
            {playbook ? (
              <TeamView playbook={playbook} />
            ) : (
              <p className="muted">No team yet, so nothing can be asked for.</p>
            )}
          </>
        )}
      </section>
      <Folders project={project} refresh={refresh} />
    </div>
  );
}

function TeamView({ playbook }: { playbook: Playbook }) {
  const code = isCode(playbook);
  return (
    <dl className="facts">
      {playbook.roles.map((role) => (
        <div key={role.name} className="fact-row">
          <dt>{role.name}</dt>
          <dd>{engineLabel(role.engine)}</dd>
        </div>
      ))}
      {code && (
        <>
          <div className="fact-row">
            <dt>QA runs</dt>
            <dd>
              <code>{playbook.check}</code>
            </dd>
          </div>
          <div className="fact-row">
            <dt>Works in</dt>
            <dd>
              A private copy of <code>{playbook.repo}</code>
            </dd>
          </div>
          <div className="fact-row">
            <dt>Commits</dt>
            <dd>{signing[playbook.sign ?? ""]}</dd>
          </div>
        </>
      )}
      {!code && (
        <div className="fact-row">
          <dt>Approved drafts</dt>
          <dd>
            {playbook.deliver_to ? (
              <>
                Copied to <code>{playbook.deliver_to}</code>
              </>
            ) : (
              "Stay on the project"
            )}
          </dd>
        </div>
      )}
      <div className="fact-row">
        <dt>Rounds</dt>
        <dd>Up to {playbook.max_rounds}, then it asks you</dd>
      </div>
    </dl>
  );
}

const firstEngine = (
  roles: Role[] | undefined,
  kind: string,
  fallback: string,
) => roles?.find((r) => r.kind === kind)?.engine ?? fallback;

function TeamEditor({
  project,
  onDone,
  refresh,
}: {
  project: Project;
  onDone: () => void;
  refresh: () => Promise<void>;
}) {
  const playbook = project.playbook;
  const folders = project.directories ?? [];
  const [template, setTemplate] = useState(playbook?.template ?? "draft");
  const code = template === "code";
  const [repo, setRepo] = useState(playbook?.repo ?? folders[0] ?? "");
  const [branchPrefix, setBranchPrefix] = useState(
    playbook?.branch_prefix ?? "crew/",
  );
  const [check, setCheck] = useState(playbook?.check ?? "");
  const [prepare, setPrepare] = useState((playbook?.prepare ?? []).join(", "));
  const [sign, setSign] = useState(playbook?.sign ?? "");
  const [writer, setWriter] = useState(
    firstEngine(playbook?.roles, "implementer", "claude"),
  );
  const [reviewer, setReviewer] = useState(
    firstEngine(playbook?.roles, "reviewer", "codex"),
  );
  const [rounds, setRounds] = useState(String(playbook?.max_rounds ?? 3));
  const [deliverTo, setDeliverTo] = useState(playbook?.deliver_to ?? "");
  const [picking, setPicking] = useState(false);
  const { busy, error, run } = useAction();
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await setTeam(project.id, {
        template,
        writer_engine: writer,
        reviewer_engine: reviewer,
        max_rounds: rounds,
        ...(code
          ? {
              deliver_to: "",
              repo,
              branch_prefix: branchPrefix.trim(),
              check: check.trim(),
              prepare: prepare
                .split(/[,\n]/)
                .map((p) => p.trim())
                .filter(Boolean),
              sign,
            }
          : { deliver_to: deliverTo }),
      });
      await refresh();
      onDone();
    });
  }
  return (
    <>
      <form className="form" onSubmit={save} aria-label="Team">
        <h2>Team</h2>
        {folders.length > 0 && (
          <label htmlFor="team-kind">
            Kind of work
            <select
              id="team-kind"
              className="field"
              value={template}
              onChange={(e) => setTemplate(e.target.value)}
            >
              <option value="draft">Writing</option>
              <option value="code">Code</option>
            </select>
          </label>
        )}
        <div className="form-row">
          <EngineSelect
            id="team-writer"
            label={code ? "Implementer" : "Writer"}
            value={writer}
            onChange={setWriter}
          />
          <EngineSelect
            id="team-reviewer"
            label="Reviewer"
            value={reviewer}
            onChange={setReviewer}
          />
          <label htmlFor="team-rounds">
            Rounds before asking you
            <input
              id="team-rounds"
              className="field"
              type="number"
              min={1}
              max={10}
              value={rounds}
              onChange={(e) => setRounds(e.target.value)}
              required
            />
          </label>
        </div>
        {code ? (
          <div className="form-row">
            <label htmlFor="team-repo">
              Repository
              <select
                id="team-repo"
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
            <label htmlFor="team-check">
              QA runs
              <input
                id="team-check"
                className="field"
                value={check}
                placeholder="make check"
                onChange={(e) => setCheck(e.target.value)}
                required
              />
            </label>
            <label htmlFor="team-prefix">
              Branch prefix
              <input
                id="team-prefix"
                className="field"
                value={branchPrefix}
                onChange={(e) => setBranchPrefix(e.target.value)}
                required
              />
            </label>
            <label htmlFor="team-prepare">
              Ignored folders to copy in
              <input
                id="team-prepare"
                className="field"
                value={prepare}
                placeholder="node_modules"
                onChange={(e) => setPrepare(e.target.value)}
              />
              <span className="hint">Optional. Separate with commas.</span>
            </label>
            <label htmlFor="team-sign">
              Sign commits
              <select
                id="team-sign"
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
        ) : (
          <div className="control">
            Copy approved drafts to
            <div className="actions">
              <code className="folder-choice">
                {deliverTo || "Nowhere; keep them on the project"}
              </code>
              <button
                type="button"
                className="btn btn-sm"
                disabled={busy}
                onClick={() => setPicking(true)}
              >
                Choose folder
              </button>
              {deliverTo && (
                <button
                  type="button"
                  className="btn btn-quiet btn-sm"
                  disabled={busy}
                  onClick={() => setDeliverTo("")}
                >
                  Clear
                </button>
              )}
            </div>
          </div>
        )}
        <p className="hint">
          Requests already under way keep the team they started with.
        </p>
        <ErrorNotice error={error} />
        <div className="actions">
          <button className="btn btn-primary" type="submit" disabled={busy}>
            Save team
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
      {picking && (
        <FileSystemPicker
          kind="directory"
          initialPath={deliverTo || project.directories?.[0]}
          onCancel={() => setPicking(false)}
          onSelect={(paths) => {
            if (paths[0]) setDeliverTo(paths[0]);
            setPicking(false);
          }}
        />
      )}
    </>
  );
}

function EngineSelect({
  id,
  label,
  value,
  onChange,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <label htmlFor={id}>
      {label}
      <select
        id={id}
        className="field"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      >
        {engines.map((engine) => (
          <option key={engine.id} value={engine.id}>
            {engine.label}
          </option>
        ))}
      </select>
    </label>
  );
}

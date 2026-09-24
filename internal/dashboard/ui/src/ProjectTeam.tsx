import { useState, type FormEvent } from "react";
import { FileSystemPicker } from "./FileSystemPicker";
import { ErrorNotice, useAction } from "./ui";
import { LandingCard } from "./ProjectLanding";
import { setTeam, type Playbook, type Project, type Role } from "./api";

const engines = [
  { id: "claude", label: "Claude" },
  { id: "codex", label: "Codex" },
];
const engineLabel = (id: string) =>
  engines.find((e) => e.id === id)?.label ?? id;

export function TeamCard({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const playbook = project.playbook;
  return (
    <section className="project-card" aria-label="Team">
      <div className="section-heading">
        <h2>Team</h2>
        {!editing && playbook && (
          <button
            type="button"
            className="text-button"
            onClick={() => setEditing(true)}
          >
            Edit team
          </button>
        )}
      </div>
      {editing ? (
        <TeamEditor
          project={project}
          onDone={() => setEditing(false)}
          refresh={refresh}
        />
      ) : playbook ? (
        <>
          <TeamView playbook={playbook} />
          {playbook.medium === "git" && (
            <LandingCard project={project} refresh={refresh} />
          )}
        </>
      ) : (
        <div className="team-prompt">
          <p className="muted">
            No one is doing this project’s work yet. For written work, a writer
            drafts and a reviewer checks each draft against the brief.
          </p>
          <button
            type="button"
            className="button primary"
            onClick={() => setEditing(true)}
          >
            Choose a team
          </button>
        </div>
      )}
    </section>
  );
}

function TeamView({ playbook }: { playbook: Playbook }) {
  const rounds = `Up to ${playbook.max_rounds} ${playbook.max_rounds === 1 ? "round" : "rounds"} before checking with you.`;
  return (
    <>
      <ul className="team-roles">
        {playbook.roles.map((role) => (
          <li key={role.name}>
            <strong>{role.name}</strong>
            <span>{engineLabel(role.engine)}</span>
          </li>
        ))}
      </ul>
      {playbook.medium === "git" ? (
        <p className="field-hint">
          Works in a private copy of <code>{playbook.repo}</code>; QA runs{" "}
          <code>{playbook.check}</code>. {rounds}
        </p>
      ) : (
        <p className="field-hint">
          {rounds}{" "}
          {playbook.deliver_to
            ? `Approved deliverables are copied to ${playbook.deliver_to}.`
            : "Approved deliverables stay on this page."}
        </p>
      )}
    </>
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
            }
          : { deliver_to: deliverTo }),
      });
      await refresh();
      onDone();
    });
  }
  return (
    <>
      <form className="project-card-form" onSubmit={save}>
        {folders.length > 0 && (
          <label htmlFor="team-kind">
            Kind of work
            <select
              id="team-kind"
              value={template}
              onChange={(e) => setTemplate(e.target.value)}
            >
              <option value="draft">Writing</option>
              <option value="code">Code in a linked folder</option>
            </select>
          </label>
        )}
        <p className="field-hint">
          {code
            ? "An implementer changes a private copy of the repository, a reviewer reads the change, and QA runs your check. Work already under way keeps the team it started with."
            : "A writer drafts and a reviewer checks each draft against the brief. Work already under way keeps the team it started with."}
        </p>
        <div className="team-fields">
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
            Rounds before checking with you
            <input
              id="team-rounds"
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
          <div className="team-fields">
            <label htmlFor="team-repo">
              Repository
              <select
                id="team-repo"
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
              Check QA runs
              <input
                id="team-check"
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
                value={branchPrefix}
                onChange={(e) => setBranchPrefix(e.target.value)}
                required
              />
            </label>
            <label htmlFor="team-prepare">
              Ignored folders to copy in (optional)
              <input
                id="team-prepare"
                value={prepare}
                placeholder="node_modules"
                onChange={(e) => setPrepare(e.target.value)}
              />
            </label>
          </div>
        ) : (
          <div className="team-delivery">
            <span>Deliver approved work to</span>
            <code>{deliverTo || "Nowhere; keep it on this page"}</code>
            <div className="form-actions">
              <button
                type="button"
                className="button secondary"
                disabled={busy}
                onClick={() => setPicking(true)}
              >
                Choose folder
              </button>
              {deliverTo && (
                <button
                  type="button"
                  className="text-button"
                  disabled={busy}
                  onClick={() => setDeliverTo("")}
                >
                  Clear
                </button>
              )}
            </div>
          </div>
        )}
        <ErrorNotice error={error} />
        <div className="form-actions">
          <button className="button primary" type="submit" disabled={busy}>
            {busy ? "Saving…" : "Save team"}
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
      <select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
        {engines.map((engine) => (
          <option key={engine.id} value={engine.id}>
            {engine.label}
          </option>
        ))}
      </select>
    </label>
  );
}

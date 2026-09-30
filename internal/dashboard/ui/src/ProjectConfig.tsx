import { useState, type FormEvent } from "react";
import { Folders } from "./ProjectFolders";
import { RunRecipeSettings } from "./RunRecipe";
import { LandingSettings } from "./ProjectLanding";
import { TeamSettings } from "./TeamSettings";
import { boardColumns, readyLabel } from "./boardLanes";
import { isCode, stageLimit, stageRole } from "./stages";
import { ErrorNotice, useAction } from "./ui";
import {
  setParallel,
  setPrefix,
  setStageLimits,
  setTitle,
  setWorkspace,
  type Member,
  type Playbook,
  type Project,
  type Stage,
} from "./api";

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
      <ProjectName project={project} refresh={refresh} />
      <TeamSettings project={project} members={members} refresh={refresh} />
      {playbook && (
        <>
          <TasksAtOnce
            project={project}
            playbook={playbook}
            refresh={refresh}
          />
        </>
      )}
      {code && (
        <>
          <LandingSettings
            project={project}
            playbook={playbook}
            refresh={refresh}
          />
          <Workspace project={project} playbook={playbook} refresh={refresh} />
          <RunRecipeSettings
            project={project}
            playbook={playbook}
            refresh={refresh}
          />
        </>
      )}
      <Folders project={project} refresh={refresh} />
      <TaskIDs project={project} refresh={refresh} />
    </div>
  );
}

/** The most requests a project may have under way at once, as the server allows. */
const mostAtOnce = 10;

/** The stages of the project's board that can have a limit. */
const limitStages = (project: Project) => [
  ...boardColumns(project, [])
    .flatMap((c) => c.lanes)
    .filter((l) => l.stage !== "todo" && l.stage !== "triage"),
  { stage: "ready" as Stage, label: readyLabel(project) },
];

/** Stage capacities and the optional overall limit. */
function TasksAtOnce({
  project,
  playbook,
  refresh,
}: {
  project: Project;
  playbook: Playbook;
  refresh: () => Promise<void>;
}) {
  const stages = limitStages(project);
  const [saved, setSaved] = useState<{
    source: Playbook;
    actual: Playbook;
  } | null>(null);
  const shown = saved?.source === playbook ? saved.actual : playbook;
  const current = () =>
    Object.fromEntries(
      stages.map((s) => [s.stage, String(shown.stage_limits?.[s.stage] || 0)]),
    );
  const [editing, setEditing] = useState(false);
  const [values, setValues] = useState<Record<string, string>>(current);
  const [value, setValue] = useState(String(playbook.max_active || 0));
  const { busy, error, run } = useAction();
  const defaults = { ...playbook, stage_limits: {} };
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      const limits = { ...shown.stage_limits };
      for (const s of stages) {
        delete limits[s.stage];
        const n = Number(values[s.stage] || 0);
        if (n > 0) limits[s.stage] = n;
      }
      let actual = shown;
      try {
        if (
          stages.some(
            (s) =>
              Number(values[s.stage] || 0) !==
              (shown.stage_limits?.[s.stage] || 0),
          )
        ) {
          await setStageLimits(project.id, limits);
          actual = { ...actual, stage_limits: limits };
        }
        if (Number(value) !== (shown.max_active || 0)) {
          await setParallel(project.id, Number(value));
          actual = { ...actual, max_active: Number(value) };
        }
      } finally {
        setSaved({ source: playbook, actual });
        setEditing(false);
        await refresh();
      }
    });
  }
  const options = Array.from({ length: mostAtOnce }, (_, i) => (
    <option key={i + 1} value={String(i + 1)}>
      {i + 1}
    </option>
  ));
  return (
    <section className="tab-panel card" aria-label="Tasks at once">
      {editing ? (
        <form className="form" aria-label="Tasks at once" onSubmit={save}>
          <h2>Tasks at once</h2>
          <div className="form-row">
            {stages.map((s) => (
              <label key={s.stage} htmlFor={`config-stage-${s.stage}`}>
                {s.label}
                <select
                  id={`config-stage-${s.stage}`}
                  className="field"
                  value={values[s.stage] || "0"}
                  onChange={(e) =>
                    setValues({ ...values, [s.stage]: e.target.value })
                  }
                >
                  <option value="0">
                    {stageLimit(defaults, s.stage)
                      ? `One per seat (${stageLimit(defaults, s.stage)})`
                      : "No limit"}
                  </option>
                  {options}
                </select>
              </label>
            ))}
            <label htmlFor="config-max-active">
              In all
              <select
                id="config-max-active"
                className="field"
                value={value}
                onChange={(e) => setValue(e.target.value)}
              >
                <option value="0">No overall limit</option>
                {options}
              </select>
            </label>
          </div>
          <p className="hint">
            A request done with a stage waits in it until the next has room. To
            do has no limit. Requests past an overall limit wait on the to-do
            list. Requests waiting on you or outside checks don't count toward
            seat-based stage limits or the overall limit, but keep counting
            toward stage limits you set.
          </p>
          <div className="actions">
            <button className="btn btn-primary" type="submit" disabled={busy}>
              Save
            </button>
            <button
              className="btn btn-quiet"
              type="button"
              disabled={busy}
              onClick={() => setEditing(false)}
            >
              Cancel
            </button>
          </div>
        </form>
      ) : (
        <>
          <div className="panel-head">
            <h2>Tasks at once</h2>
            <button
              type="button"
              className="btn btn-sm"
              onClick={() => {
                setValues(current());
                setValue(String(shown.max_active || 0));
                setEditing(true);
              }}
            >
              Edit
            </button>
          </div>
          <dl className="facts">
            {stages.map((s) => {
              const limit = stageLimit(shown, s.stage);
              return (
                <div key={s.stage} className="fact-row">
                  <dt>{s.label}</dt>
                  <dd>
                    {limit
                      ? `Up to ${limit} at once (${
                          shown.stage_limits?.[s.stage]
                            ? "set by you"
                            : `one per ${s.stage === "qa" ? s.label : stageRole[s.stage]} seat`
                        })`
                      : "No limit"}
                  </dd>
                </div>
              );
            })}
            <div className="fact-row">
              <dt>In all</dt>
              <dd>
                {shown.max_active
                  ? `Up to ${shown.max_active} at once`
                  : "No limit beyond the stages"}
              </dd>
            </div>
          </dl>
        </>
      )}
      <ErrorNotice error={error} />
    </section>
  );
}

/** The longest name a project can have, as the server allows. */
const longestName = 200;

/** The project's name. Renaming it keeps its request IDs as they are. */
function ProjectName({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const [title, setTitleText] = useState(project.title);
  const { busy, error, run } = useAction();
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await setTitle(project.id, title);
      await refresh();
      setEditing(false);
    });
  }
  if (editing)
    return (
      <section className="tab-panel card" aria-label="Name">
        <form className="form" aria-label="Name" onSubmit={save}>
          <h2>Name</h2>
          <label htmlFor="config-project-title">
            Name
            <input
              id="config-project-title"
              className="field"
              value={title}
              maxLength={longestName}
              onChange={(e) => setTitleText(e.target.value)}
              required
            />
            <span className="hint">
              Request IDs keep their prefix whatever the project is called.
            </span>
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
              onClick={() => {
                setTitleText(project.title);
                setEditing(false);
              }}
            >
              Cancel
            </button>
          </div>
        </form>
      </section>
    );
  return (
    <section className="tab-panel card" aria-label="Name">
      <div className="panel-head">
        <h2>Name</h2>
        <button
          type="button"
          className="btn btn-sm"
          onClick={() => {
            setTitleText(project.title);
            setEditing(true);
          }}
        >
          Edit
        </button>
      </div>
      <dl className="facts">
        <div className="fact-row">
          <dt>Name</dt>
          <dd>{project.title}</dd>
        </div>
      </dl>
    </section>
  );
}

/**
 * The prefix of the project's readable request IDs. Renaming it renames
 * every request's ID; links keep working, since they hold the full ID.
 */
function TaskIDs({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const [prefix, setPrefixText] = useState(project.prefix ?? "");
  const { busy, error, run } = useAction();
  const shown = prefix.trim().toUpperCase() || project.prefix || "CA";
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await setPrefix(project.id, prefix);
      await refresh();
      setEditing(false);
    });
  }
  if (editing)
    return (
      <section className="tab-panel card" aria-label="Request IDs">
        <form className="form" aria-label="Request IDs" onSubmit={save}>
          <h2>Request IDs</h2>
          <label htmlFor="config-task-prefix">
            Prefix
            <input
              id="config-task-prefix"
              className="field"
              value={prefix}
              maxLength={6}
              pattern="[A-Za-z][A-Za-z0-9]{0,5}"
              onChange={(e) => setPrefixText(e.target.value)}
              required
            />
            <span className="hint">
              1 to 6 letters or digits, starting with a letter. Requests read as{" "}
              {shown}-1, {shown}-2, and so on.
            </span>
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
              onClick={() => {
                setPrefixText(project.prefix ?? "");
                setEditing(false);
              }}
            >
              Cancel
            </button>
          </div>
        </form>
      </section>
    );
  return (
    <section className="tab-panel card" aria-label="Request IDs">
      <div className="panel-head">
        <h2>Request IDs</h2>
        <button
          type="button"
          className="btn btn-sm"
          onClick={() => {
            setPrefixText(project.prefix ?? "");
            setEditing(true);
          }}
        >
          Edit
        </button>
      </div>
      <dl className="facts">
        <div className="fact-row">
          <dt>Prefix</dt>
          <dd>
            {project.prefix ? (
              <>
                <code>{project.prefix}</code>, as in{" "}
                <code>{project.prefix}-1</code>
              </>
            ) : (
              "None yet"
            )}
          </dd>
        </div>
      </dl>
    </section>
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

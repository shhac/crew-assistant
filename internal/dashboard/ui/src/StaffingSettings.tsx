import { useState } from "react";
import { Panel } from "./SettingsPanel";
import { holds, kindLabel, kindsLabel, memberKinds } from "./members";
import {
  areasText,
  parseAreas,
  pathList,
  repositoryUsers,
  teamUsers,
} from "./staffing";
import { ErrorNotice, useAction } from "./ui";
import {
  addTeamSeat,
  createRepository,
  createTeam,
  deleteRepository,
  deleteTeam,
  removeTeamSeat,
  setTeamSeat,
  updateRepository,
  updateTeam,
  type MemberKind,
  type Project,
  type Repository,
  type RepositoryInput,
  type State,
  type Team,
} from "./api";

/** Who else a shared repository or team reaches, said before it is changed. */
function UsedBy({ users, what }: { users: Project[]; what: string }) {
  if (users.length === 0)
    return <p className="muted">No project uses this {what} yet.</p>;
  const titles = users.map((p) => p.title).join(", ");
  if (users.length === 1) return <p className="muted">Used by {titles}.</p>;
  return (
    <p className="hint" role="note">
      Shared by {users.length} projects ({titles}): a change here reaches all of
      them.
    </p>
  );
}

const emptyRepository: RepositoryInput = { name: "", path: "" };

/**
 * The repositories projects work in: each one's check, what is copied into
 * a private copy, how QA runs the app and its code areas. Several projects
 * can share one.
 */
export function RepositoriesSettings({
  state,
  refresh,
}: {
  state: State;
  refresh: () => Promise<void>;
}) {
  const repositories = state.repositories ?? [];
  const [adding, setAdding] = useState(false);
  return (
    <>
      {repositories.map((r) => (
        <RepositoryCard
          key={r.id}
          repository={r}
          users={repositoryUsers(state, r.id)}
          refresh={refresh}
        />
      ))}
      {repositories.length === 0 && (
        <p className="muted">
          No repositories yet. Choosing a code team for a project registers its
          repository; you can also add one here.
        </p>
      )}
      <Panel title="Add a repository">
        {adding ? (
          <RepositoryEditor
            initial={emptyRepository}
            onSave={async (input) => {
              await createRepository(input);
              await refresh();
              setAdding(false);
            }}
            onCancel={() => setAdding(false)}
          />
        ) : (
          <button
            type="button"
            className="btn btn-sm"
            onClick={() => setAdding(true)}
          >
            Add a repository
          </button>
        )}
      </Panel>
    </>
  );
}

function RepositoryCard({
  repository: r,
  users,
  refresh,
}: {
  repository: Repository;
  users: Project[];
  refresh: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const removing = useAction();
  return (
    <section className="tab-panel card settings-panel" aria-label={r.name}>
      <div className="panel-head">
        <h2>{r.name}</h2>
        {!editing && (
          <button
            type="button"
            className="btn btn-sm"
            onClick={() => setEditing(true)}
          >
            Edit
          </button>
        )}
      </div>
      <UsedBy users={users} what="repository" />
      {editing ? (
        <RepositoryEditor
          initial={r}
          onSave={async (input) => {
            await updateRepository(r.id, input);
            await refresh();
            setEditing(false);
          }}
          onCancel={() => setEditing(false)}
        />
      ) : (
        <>
          <dl className="facts">
            <div className="fact-row">
              <dt>Local clone</dt>
              <dd>
                <code>{r.path}</code>
              </dd>
            </div>
            <div className="fact-row">
              <dt>Default branch</dt>
              <dd>
                {r.default_branch ? (
                  <code>{r.default_branch}</code>
                ) : (
                  "As each project's landing says"
                )}
              </dd>
            </div>
            <div className="fact-row">
              <dt>Check</dt>
              <dd>
                {r.check ? <code>{r.check}</code> : "None"}
                {r.check_in_copy && " · in a writable copy"}
                {r.check_loopback && " · may use this machine's addresses"}
              </dd>
            </div>
            <div className="fact-row">
              <dt>Copied in</dt>
              <dd>
                {r.prepare?.length ? (
                  <code>{r.prepare.join(", ")}</code>
                ) : (
                  "Nothing"
                )}
              </dd>
            </div>
            <div className="fact-row">
              <dt>QA runs the app</dt>
              <dd>
                {r.run ? (
                  <>
                    <code>{r.run.start}</code> at <code>{r.run.url}</code>
                  </>
                ) : (
                  "No, only the check"
                )}
              </dd>
            </div>
            <div className="fact-row">
              <dt>Code areas</dt>
              <dd>
                {r.areas?.length
                  ? r.areas.map((a) => (
                      <div key={a.name}>
                        {a.name}: <code>{a.paths.join(", ")}</code>
                      </div>
                    ))
                  : "None"}
              </dd>
            </div>
          </dl>
          {users.length === 0 && (
            <div className="actions">
              <button
                type="button"
                className="btn btn-sm btn-quiet"
                disabled={removing.busy}
                onClick={() =>
                  void removing.run(async () => {
                    await deleteRepository(r.id);
                    await refresh();
                  })
                }
              >
                Remove
              </button>
            </div>
          )}
          <ErrorNotice error={removing.error} />
        </>
      )}
    </section>
  );
}

/** The fields of a repository; the run recipe is kept as it is. */
function RepositoryEditor({
  initial,
  onSave,
  onCancel,
}: {
  initial: RepositoryInput;
  onSave: (input: RepositoryInput) => Promise<void>;
  onCancel: () => void;
}) {
  const [name, setName] = useState(initial.name);
  const [path, setPath] = useState(initial.path);
  const [branch, setBranch] = useState(initial.default_branch ?? "");
  const [check, setCheck] = useState(initial.check ?? "");
  const [prepare, setPrepare] = useState((initial.prepare ?? []).join(", "));
  const [inCopy, setInCopy] = useState(!!initial.check_in_copy);
  const [loopback, setLoopback] = useState(!!initial.check_loopback);
  const [areas, setAreas] = useState(areasText(initial));
  const { busy, error, run } = useAction();
  const id = initial.path ? `repo-${initial.path}` : "repo-new";
  return (
    <div className="form" role="group" aria-label="Repository">
      <div className="form-row">
        <label htmlFor={`${id}-name`}>
          Name
          <input
            id={`${id}-name`}
            className="field"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        <label htmlFor={`${id}-path`}>
          Local clone
          <input
            id={`${id}-path`}
            className="field"
            value={path}
            placeholder="/Users/you/code/app"
            onChange={(e) => setPath(e.target.value)}
          />
          <span className="hint">
            A project works in it only once this folder is linked to the
            project.
          </span>
        </label>
        <label htmlFor={`${id}-branch`}>
          Default branch
          <input
            id={`${id}-branch`}
            className="field"
            value={branch}
            placeholder="main"
            onChange={(e) => setBranch(e.target.value)}
          />
        </label>
        <label htmlFor={`${id}-check`}>
          Check
          <input
            id={`${id}-check`}
            className="field"
            value={check}
            placeholder="make check"
            onChange={(e) => setCheck(e.target.value)}
          />
        </label>
        <label htmlFor={`${id}-prepare`}>
          Ignored folders to copy in
          <input
            id={`${id}-prepare`}
            className="field"
            value={prepare}
            placeholder="**/node_modules"
            onChange={(e) => setPrepare(e.target.value)}
          />
          <span className="hint">Optional. Separate with commas.</span>
        </label>
      </div>
      <label>
        <input
          type="checkbox"
          checked={inCopy}
          onChange={(e) => setInCopy(e.target.checked)}
        />
        Run the check in a writable copy
      </label>
      <label>
        <input
          type="checkbox"
          checked={loopback}
          onChange={(e) => setLoopback(e.target.checked)}
        />
        Let the check use this machine's own addresses
      </label>
      <label htmlFor={`${id}-areas`}>
        Code areas
        <textarea
          id={`${id}-areas`}
          className="field"
          rows={3}
          value={areas}
          placeholder="web: apps/web/**, packages/ui"
          onChange={(e) => setAreas(e.target.value)}
        />
        <span className="hint">
          One per line, as name: paths. Projects name the areas they expect to
          touch.
        </span>
      </label>
      <ErrorNotice error={error} />
      <div className="actions">
        <button
          type="button"
          className="btn btn-primary"
          disabled={busy}
          onClick={() =>
            void run(() =>
              onSave({
                ...initial,
                name,
                path,
                default_branch: branch,
                check,
                prepare: pathList(prepare),
                check_in_copy: inCopy,
                check_loopback: loopback,
                areas: parseAreas(areas),
              }),
            )
          }
        >
          Save
        </button>
        <button
          type="button"
          className="btn btn-quiet"
          disabled={busy}
          onClick={onCancel}
        >
          Cancel
        </button>
      </div>
    </div>
  );
}

const templates = [
  { id: "code", label: "Code" },
  { id: "draft", label: "Writing" },
];

/**
 * Teams: default seats by kind of role, a PM among them, and the rounds
 * before the PM escalates. Projects a team staffs take its seats, with their
 * own overrides.
 */
export function TeamsSettings({
  state,
  refresh,
}: {
  state: State;
  refresh: () => Promise<void>;
}) {
  const teams = state.teams ?? [];
  const [name, setName] = useState("");
  const [template, setTemplate] = useState("code");
  const { busy, error, run } = useAction();
  return (
    <>
      {teams.map((t) => (
        <TeamCard
          key={t.id}
          team={t}
          state={state}
          users={teamUsers(state, t.id)}
          refresh={refresh}
        />
      ))}
      {teams.length === 0 && (
        <p className="muted">
          No teams yet. A project's seats can become a team from its Config tab.
        </p>
      )}
      <Panel title="Add a team">
        <div className="form" role="group" aria-label="Add a team">
          <div className="form-row">
            <label htmlFor="team-new-name">
              Name
              <input
                id="team-new-name"
                className="field"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
            <label htmlFor="team-new-template">
              Kind of work
              <select
                id="team-new-template"
                className="field"
                value={template}
                onChange={(e) => setTemplate(e.target.value)}
              >
                {templates.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.label}
                  </option>
                ))}
              </select>
            </label>
          </div>
          <ErrorNotice error={error} />
          <div className="actions">
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy || !name.trim()}
              onClick={() =>
                void run(async () => {
                  await createTeam({ name, template });
                  await refresh();
                  setName("");
                })
              }
            >
              Add team
            </button>
          </div>
        </div>
      </Panel>
    </>
  );
}

function TeamCard({
  team: t,
  state,
  users,
  refresh,
}: {
  team: Team;
  state: State;
  users: Project[];
  refresh: () => Promise<void>;
}) {
  const [name, setName] = useState(t.name);
  const [rounds, setRounds] = useState(String(t.max_rounds));
  const [kind, setKind] = useState<MemberKind>("implementer");
  const [member, setMember] = useState("");
  const { busy, error, run } = useAction();
  const act = (change: () => Promise<unknown>) =>
    void run(async () => {
      await change();
      await refresh();
    });
  const fitting = state.members.filter((m) => holds(m, kind));
  return (
    <section className="tab-panel card settings-panel" aria-label={t.name}>
      <h2>{t.name}</h2>
      <UsedBy users={users} what="team" />
      <ul className="rows" aria-label="Seats">
        {t.roles.map((r) => (
          <li key={r.name} className="integration">
            <span>
              <strong>{r.name}</strong>{" "}
              <span className="muted small">
                {kindsLabel(r.kinds)}
                {r.member ? "" : " · template seat"}
              </span>
            </span>
            <span className="actions">
              <button
                type="button"
                className="btn btn-sm"
                disabled={busy}
                onClick={() => act(() => addTeamSeat(t.id, { seat: r.name }))}
              >
                Add one like it
              </button>
              <button
                type="button"
                className="btn btn-sm btn-quiet"
                disabled={busy}
                onClick={() => act(() => removeTeamSeat(t.id, r.name))}
              >
                Remove
              </button>
            </span>
          </li>
        ))}
      </ul>
      <div
        className="form"
        role="group"
        aria-label={`Fill a role on ${t.name}`}
      >
        <div className="form-row">
          <label htmlFor={`team-${t.id}-kind`}>
            Role
            <select
              id={`team-${t.id}-kind`}
              className="field"
              value={kind}
              onChange={(e) => {
                setKind(e.target.value as MemberKind);
                setMember("");
              }}
            >
              {memberKinds.map((k) => (
                <option key={k.id} value={k.id}>
                  {k.label}
                </option>
              ))}
            </select>
          </label>
          <label htmlFor={`team-${t.id}-member`}>
            Filled by
            <select
              id={`team-${t.id}-member`}
              className="field"
              value={member}
              onChange={(e) => setMember(e.target.value)}
            >
              <option value="">The template's {kindLabel(kind)}</option>
              {fitting.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name}
                </option>
              ))}
            </select>
          </label>
        </div>
        <div className="actions">
          <button
            type="button"
            className="btn btn-sm"
            disabled={busy}
            onClick={() => act(() => setTeamSeat(t.id, kind, member))}
          >
            Give the role
          </button>
          <button
            type="button"
            className="btn btn-sm"
            disabled={busy}
            onClick={() => act(() => addTeamSeat(t.id, { kind, member }))}
          >
            Add to the role
          </button>
        </div>
        <div className="form-row">
          <label htmlFor={`team-${t.id}-name`}>
            Name
            <input
              id={`team-${t.id}-name`}
              className="field"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </label>
          <label htmlFor={`team-${t.id}-rounds`}>
            Rounds before the PM escalates
            <input
              id={`team-${t.id}-rounds`}
              className="field"
              type="number"
              min={1}
              max={10}
              value={rounds}
              onChange={(e) => setRounds(e.target.value)}
            />
          </label>
        </div>
        <ErrorNotice error={error} />
        <div className="actions">
          <button
            type="button"
            className="btn btn-primary"
            disabled={busy}
            onClick={() =>
              act(() => updateTeam(t.id, { name, max_rounds: Number(rounds) }))
            }
          >
            Save name and rounds
          </button>
          {users.length === 0 && (
            <button
              type="button"
              className="btn btn-quiet"
              disabled={busy}
              onClick={() => act(() => deleteTeam(t.id))}
            >
              Remove team
            </button>
          )}
        </div>
      </div>
    </section>
  );
}

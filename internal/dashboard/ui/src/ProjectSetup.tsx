import { useState, type FormEvent } from "react";
import { holds, kindLabel, memberKinds } from "./members";
import { isCode } from "./landing";
import { projectRepository, projectTeam, sharedWith } from "./staffing";
import { ErrorNotice, useAction } from "./ui";
import {
  createTeam,
  setProjectSetup,
  setSeatOverrides,
  type MemberKind,
  type OverrideAction,
  type Project,
  type SeatOverride,
  type SeatOverrideChoice,
  type State,
} from "./api";

/** A line saying who else a change to a shared repository or team reaches. */
export function SharedNote({
  state,
  project,
  what,
}: {
  state?: Pick<State, "projects">;
  project: Project;
  what: "repository" | "team";
}) {
  const others = sharedWith(state, project, what);
  if (others.length === 0) return null;
  return (
    <p className="hint" role="note">
      This {what} is shared with {others.join(", ")}: changes here reach{" "}
      {others.length === 1 ? "it" : "them"} too.
    </p>
  );
}

/** What an override does, as the owner reads it. */
function overrideText(o: SeatOverride) {
  const who = o.seat?.name ?? "";
  switch (o.action) {
    case "exclude":
      return `No ${kindLabel(o.kind)} on this project`;
    case "replace":
      return `${who} is this project's ${kindLabel(o.kind)}`;
    default:
      return `${who} added as ${kindLabel(o.kind)}`;
  }
}

const asChoice = (o: SeatOverride): SeatOverrideChoice => ({
  action: o.action,
  kind: o.kind,
  member: o.seat?.member ?? "",
});

/**
 * The team that staffs the project and the repository it works in, and the
 * project's own adjustments to the team's seats.
 */
export function ProjectSetupCard({
  project,
  state,
  refresh,
}: {
  project: Project;
  state: State;
  refresh: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const team = projectTeam(project, state.teams);
  const repo = projectRepository(project, state.repositories);
  const areas = project.scope?.repositories?.[0]?.areas ?? [];
  const making = useAction();
  return (
    <section className="tab-panel card" aria-label="Repository and team">
      <div className="panel-head">
        <h3>Repository and team</h3>
        {!editing && (
          <button
            type="button"
            className="btn btn-sm"
            onClick={() => setEditing(true)}
          >
            Change
          </button>
        )}
      </div>
      {editing ? (
        <SetupEditor
          project={project}
          state={state}
          onDone={() => setEditing(false)}
          refresh={refresh}
        />
      ) : (
        <>
          <dl className="facts">
            <div className="fact-row">
              <dt>Team</dt>
              <dd>{team ? team.name : "Its own seats"}</dd>
            </div>
            {project.playbook && isCode(project.playbook) && (
              <div className="fact-row">
                <dt>Repository</dt>
                <dd>
                  {repo ? (
                    <>
                      {repo.name} (<code>{repo.path}</code>)
                    </>
                  ) : (
                    "None yet"
                  )}
                </dd>
              </div>
            )}
            {repo && (
              <div className="fact-row">
                <dt>Code areas</dt>
                <dd>
                  {areas.length ? areas.join(", ") : "The whole repository"}
                </dd>
              </div>
            )}
          </dl>
          <SharedNote state={state} project={project} what="team" />
          <SharedNote state={state} project={project} what="repository" />
          {!team && project.playbook && (
            <div className="actions">
              <button
                type="button"
                className="btn btn-sm"
                disabled={making.busy}
                onClick={() =>
                  void making.run(async () => {
                    await createTeam({
                      name: project.title,
                      from_project: project.id,
                    });
                    await refresh();
                  })
                }
              >
                Make these seats a team
              </button>
            </div>
          )}
          <ErrorNotice error={making.error} />
        </>
      )}
      {project.playbook && (
        <SeatOverrides project={project} state={state} refresh={refresh} />
      )}
    </section>
  );
}

function SetupEditor({
  project,
  state,
  onDone,
  refresh,
}: {
  project: Project;
  state: State;
  onDone: () => void;
  refresh: () => Promise<void>;
}) {
  const [team, setTeam] = useState(project.team ?? "");
  const [repository, setRepository] = useState(
    project.scope?.repositories?.[0]?.id ?? "",
  );
  const [areas, setAreas] = useState<string[]>(
    project.scope?.repositories?.[0]?.areas ?? [],
  );
  const { busy, error, run } = useAction();
  const folders = project.directories ?? [];
  // Only a repository among the project's own folders can be chosen.
  const repositories = (state.repositories ?? []).filter((r) =>
    folders.includes(r.path),
  );
  const chosen = repositories.find((r) => r.id === repository);
  const code = !project.playbook || isCode(project.playbook);
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await setProjectSetup(project.id, {
        team,
        repository,
        areas: chosen ? areas : [],
      });
      await refresh();
      onDone();
    });
  }
  return (
    <form className="form" aria-label="Repository and team" onSubmit={save}>
      <div className="form-row">
        <label htmlFor="setup-team">
          Team
          <select
            id="setup-team"
            className="field"
            value={team}
            onChange={(e) => setTeam(e.target.value)}
          >
            <option value="">Its own seats</option>
            {(state.teams ?? []).map((t) => (
              <option key={t.id} value={t.id}>
                {t.name}
              </option>
            ))}
          </select>
        </label>
        {code && (
          <label htmlFor="setup-repository">
            Repository
            <select
              id="setup-repository"
              className="field"
              value={repository}
              onChange={(e) => {
                setRepository(e.target.value);
                setAreas([]);
              }}
            >
              <option value="">None</option>
              {repositories.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name} ({r.path})
                </option>
              ))}
            </select>
            <span className="hint">
              Repositories whose folder is linked to this project.
            </span>
          </label>
        )}
      </div>
      {chosen?.areas?.length ? (
        <fieldset className="form">
          <legend>Code areas it expects to touch</legend>
          {chosen.areas.map((a) => (
            <label key={a.name}>
              <input
                type="checkbox"
                checked={areas.includes(a.name)}
                onChange={(e) =>
                  setAreas(
                    e.target.checked
                      ? [...areas, a.name]
                      : areas.filter((n) => n !== a.name),
                  )
                }
              />
              {a.name} <span className="muted">{a.paths.join(", ")}</span>
            </label>
          ))}
        </fieldset>
      ) : null}
      <p className="hint">
        Leaving a team or a repository keeps its seats or code settings as the
        project's own, so work goes on as it did. Requests already under way
        keep what they started with.
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

const actions: { id: OverrideAction; label: string }[] = [
  { id: "add", label: "Add a seat" },
  { id: "replace", label: "Replace the team's" },
  { id: "exclude", label: "Leave out" },
];

/** The project's own adjustments to its team's seats, by kind of role. */
function SeatOverrides({
  project,
  state,
  refresh,
}: {
  project: Project;
  state: State;
  refresh: () => Promise<void>;
}) {
  const overrides = project.seat_overrides ?? [];
  const [action, setAction] = useState<OverrideAction>("add");
  const [kind, setKind] = useState<MemberKind>("reviewer");
  const [member, setMember] = useState("");
  const { busy, error, run } = useAction();
  const save = (next: SeatOverrideChoice[]) =>
    void run(async () => {
      await setSeatOverrides(project.id, next);
      await refresh();
    });
  const fitting = state.members.filter((m) => holds(m, kind));
  return (
    <div className="form" role="group" aria-label="Seat overrides">
      <h4>Seat overrides</h4>
      {overrides.length ? (
        <ul className="rows">
          {overrides.map((o, i) => (
            <li key={i} className="integration">
              <span>{overrideText(o)}</span>
              <button
                type="button"
                className="btn btn-sm btn-quiet"
                disabled={busy}
                onClick={() =>
                  save(overrides.filter((_, j) => j !== i).map(asChoice))
                }
              >
                Remove
              </button>
            </li>
          ))}
        </ul>
      ) : (
        <p className="muted">
          None: the project works with its team's seats as they are.
        </p>
      )}
      <div className="form-row">
        <label htmlFor="override-action">
          Change
          <select
            id="override-action"
            className="field"
            value={action}
            onChange={(e) => setAction(e.target.value as OverrideAction)}
          >
            {actions.map((a) => (
              <option key={a.id} value={a.id}>
                {a.label}
              </option>
            ))}
          </select>
        </label>
        <label htmlFor="override-kind">
          Role
          <select
            id="override-kind"
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
        {action !== "exclude" && (
          <label htmlFor="override-member">
            Filled by
            <select
              id="override-member"
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
        )}
      </div>
      <ErrorNotice error={error} />
      <div className="actions">
        <button
          type="button"
          className="btn btn-sm"
          disabled={busy}
          onClick={() =>
            save([
              ...overrides.map(asChoice),
              action === "exclude"
                ? { action, kind }
                : { action, kind, member },
            ])
          }
        >
          Add override
        </button>
      </div>
    </div>
  );
}

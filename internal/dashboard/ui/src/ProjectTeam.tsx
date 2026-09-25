import { useState, type FormEvent } from "react";
import { FileSystemPicker } from "./FileSystemPicker";
import { href, memberHref } from "./router";
import { isCode } from "./landing";
import {
  engineLabel,
  engines,
  holds,
  kindLabel,
  kindsLabel,
  memberOf,
  noResearch,
  seatFor,
  seatMember,
  teamChoice,
} from "./members";
import { Avatar } from "./Avatar";
import { ErrorNotice, useAction } from "./ui";
import {
  setSeat,
  setTeam,
  type Member,
  type MemberKind,
  type Playbook,
  type Project,
  type Role,
} from "./api";

/** What a place on the team is called: a writing team's implementer writes. */
const seatLabel = (kind: string, code: boolean) =>
  kind === "implementer" && !code ? "Writer" : kindLabel(kind);

/**
 * The roles a team has places for, in the order it works: a writing team
 * neither researches nor runs QA, and any team can have a designer and a PM.
 */
const seatKinds = (code: boolean): MemberKind[] =>
  code
    ? ["researcher", "designer", "implementer", "reviewer", "qa", "pm"]
    : ["designer", "implementer", "reviewer", "pm"];

/** What a role no member or template fills reads as. */
const nobody: Partial<Record<MemberKind, string>> = {
  designer: "No designer",
  pm: "No PM",
};

export function TeamTab({
  project,
  members,
  refresh,
}: {
  project: Project;
  members: Member[];
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
            members={members}
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
              <>
                <Seats
                  project={project}
                  playbook={playbook}
                  members={members}
                  refresh={refresh}
                />
                <TeamView playbook={playbook} />
              </>
            ) : (
              <p className="muted">No team yet, so nothing can be asked for.</p>
            )}
          </>
        )}
      </section>
    </div>
  );
}

/**
 * Each role on the team and who fills it. Members are added to and removed
 * from the owner's Team; here they are only given a role.
 */
function Seats({
  project,
  playbook,
  members,
  refresh,
}: {
  project: Project;
  playbook: Playbook;
  members: Member[];
  refresh: () => Promise<void>;
}) {
  const code = isCode(playbook);
  const { busy, error, run } = useAction();
  async function fill(kind: MemberKind, id: string) {
    await run(async () => {
      await setSeat(project.id, kind, id);
      await refresh();
    });
  }
  return (
    <>
      <ul className="seats rows" aria-label="Roles">
        {seatKinds(code).map((kind) => (
          <Seat
            key={kind}
            kind={kind}
            label={seatLabel(kind, code)}
            playbook={playbook}
            members={members}
            busy={busy}
            onFill={(id) => void fill(kind, id)}
          />
        ))}
      </ul>
      <ErrorNotice error={error} />
      <p className="hint">
        Add or remove people on <a href={href({ page: "team" })}>your team</a>.
        Requests already under way keep the team they started with.
      </p>
    </>
  );
}

/**
 * One role: the member in it, the template's seat, or no one. Only research
 * can be left out of a team that has it, and no template has a designer or a
 * PM.
 */
function Seat({
  kind,
  label,
  playbook,
  members,
  busy,
  onFill,
}: {
  kind: MemberKind;
  label: string;
  playbook: Playbook;
  members: Member[];
  busy: boolean;
  onFill: (id: string) => void;
}) {
  const role = seatFor(playbook, kind);
  const filled = seatMember(playbook, kind, members);
  // Only a member who holds the kind can be given the role; one given it
  // before its kinds changed keeps it until the owner changes it.
  const eligible = members.filter((m) => holds(m, kind));
  const kept = !!filled && !holds(filled, kind);
  const options = filled && kept ? [...eligible, filled] : eligible;
  const optional = kind === "researcher";
  const empty = nobody[kind] ?? "Template default";
  const value = filled?.id ?? (optional && !role ? noResearch : "");
  return (
    <li className="seat">
      <span className="seat-role">{label}</span>
      <span className="seat-who">
        {filled && role ? (
          <RoleName role={role} members={members} />
        ) : role ? (
          <span className="muted">
            Template default · {engineLabel(role.engine)}
          </span>
        ) : (
          <span className="muted">{optional ? "No research" : empty}</span>
        )}
        {filled && role && role.kinds.length > 1 && (
          <span className="muted small"> · {seatSummary(role)}</span>
        )}
        {kept && (
          <span className="muted small">
            {" "}
            · Kept here; no longer holds this role on your team
          </span>
        )}
      </span>
      <span className="seat-actions">
        {options.length > 0 || optional ? (
          <select
            className="field"
            aria-label={`Assign ${label}`}
            value={value}
            disabled={busy}
            onChange={(e) => onFill(e.target.value)}
          >
            <option value="">{empty}</option>
            {optional && <option value={noResearch}>No research</option>}
            {options.map((m) => (
              <option key={m.id} value={m.id} disabled={kept && m === filled}>
                {m.name}
              </option>
            ))}
          </select>
        ) : (
          <span className="muted small">No one to assign</span>
        )}
        {role?.member && (
          <button
            type="button"
            className="btn btn-quiet btn-sm"
            disabled={busy}
            onClick={() => onFill("")}
          >
            Unassign {label}
          </button>
        )}
      </span>
    </li>
  );
}

function TeamView({ playbook }: { playbook: Playbook }) {
  const code = isCode(playbook);
  return (
    <dl className="facts">
      {code && (
        <div className="fact-row">
          <dt>QA runs</dt>
          <dd>
            <code>{playbook.check}</code>
          </dd>
        </div>
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

/** "Researcher and implementer · Claude": every role a seat holds. */
function seatSummary(role: Role) {
  return [kindsLabel(role.kinds), engineLabel(role.engine)].join(" · ");
}

/**
 * A role by the name it was given when the team was chosen. Verdicts are
 * recorded against that name, so a member renamed since keeps its old one
 * here.
 */
function RoleName({ role, members }: { role: Role; members: Member[] }) {
  const member = memberOf(role, members);
  if (!member) return <>{role.name}</>;
  return (
    <a className="role-member" href={memberHref(member.id)}>
      <Avatar of={member} size={20} />
      {role.name}
    </a>
  );
}

/**
 * The kind of team and how it works. Who fills each role is chosen beside
 * it, and where the work happens is on the Config tab; both are kept as they
 * are here.
 */
function TeamEditor({
  project,
  members,
  onDone,
  refresh,
}: {
  project: Project;
  members: Member[];
  onDone: () => void;
  refresh: () => Promise<void>;
}) {
  const playbook = project.playbook;
  const current = playbook && teamChoice(playbook, members);
  const folders = project.directories ?? [];
  const [template, setTemplate] = useState(playbook?.template ?? "draft");
  const code = template === "code";
  const [check, setCheck] = useState(playbook?.check ?? "");
  const [writer, setWriter] = useState(current?.writer_engine || "claude");
  const [reviewer, setReviewer] = useState(current?.reviewer_engine || "codex");
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
        implementer_member: current?.implementer_member ?? "",
        reviewer_member: current?.reviewer_member ?? "",
        qa_member: code ? (current?.qa_member ?? "") : "",
        researcher_member: code ? (current?.researcher_member ?? "") : "",
        designer_member: current?.designer_member ?? "",
        pm_member: current?.pm_member ?? "",
        max_rounds: rounds,
        ...(code
          ? {
              deliver_to: "",
              repo: current?.repo ?? "",
              branch_prefix: current?.branch_prefix ?? "",
              check: check.trim(),
              prepare: current?.prepare ?? [],
              sign: current?.sign ?? "",
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
          {!current?.implementer_member && (
            <EngineSlot
              label={code ? "Implementer" : "Writer"}
              id="team-writer"
              value={writer}
              onChange={setWriter}
            />
          )}
          {!current?.reviewer_member && (
            <EngineSlot
              label="Reviewer"
              id="team-reviewer"
              value={reviewer}
              onChange={setReviewer}
            />
          )}
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

/**
 * The engine a template's role runs on. A member brings its own, so this is
 * asked only of a role no member fills.
 */
function EngineSlot({
  label,
  id,
  value,
  onChange,
}: {
  label: string;
  id: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <fieldset className="team-slot">
      <legend>{label}</legend>
      <label htmlFor={id}>
        Engine
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
    </fieldset>
  );
}

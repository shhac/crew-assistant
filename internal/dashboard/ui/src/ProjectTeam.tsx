import { href, memberHref, projectHref } from "./router";
import { isCode } from "./landing";
import {
  engineLabel,
  holds,
  kindLabel,
  kindsLabel,
  memberOf,
  noResearch,
  seatFor,
  seatMember,
} from "./members";
import { Avatar } from "./Avatar";
import { ErrorNotice, useAction } from "./ui";
import {
  setSeat,
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
  const playbook = project.playbook;
  return (
    <div className="tab-stack">
      <section className="tab-panel card" aria-label="Team">
        <h2>Team</h2>
        {playbook ? (
          <>
            <Seats
              project={project}
              playbook={playbook}
              members={members}
              refresh={refresh}
            />
            <MemberRoles playbook={playbook} members={members} />
          </>
        ) : (
          <p className="muted">
            No team yet, so nothing can be asked for.{" "}
            <a href={projectHref(project.id, "config")}>
              Choose a team in Config
            </a>
          </p>
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

/** What each member on this team does here, which may be several roles. */
function MemberRoles({
  playbook,
  members,
}: {
  playbook: Playbook;
  members: Member[];
}) {
  const filled = playbook.roles.filter((r) => memberOf(r, members));
  if (!filled.length) return null;
  return (
    <div className="section">
      <p className="label">Members on this team</p>
      <dl className="facts" aria-label="Members on this team">
        {filled.map((role) => (
          <div key={role.name} className="fact-row">
            <dt>
              <RoleName role={role} members={members} />
            </dt>
            <dd>{kindsLabel(role.kinds)}</dd>
          </div>
        ))}
      </dl>
    </div>
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

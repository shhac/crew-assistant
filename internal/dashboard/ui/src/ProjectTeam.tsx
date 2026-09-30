import { useState } from "react";
import { href, memberHref, projectHref, requestHref } from "./router";
import { isCode } from "./landing";
import { engineLabel } from "./engines";
import { boardColumns, readyLabel } from "./boardLanes";
import { finished, projectTasks } from "./stages";
import {
  holds,
  kindLabel,
  kindsLabel,
  memberOf,
  noResearch,
  seatFor,
  seatMember,
} from "./members";
import { Avatar } from "./Avatar";
import { QABrowser } from "./QABrowser";
import { ErrorNotice, useAction } from "./ui";
import {
  addSeat,
  removeSeat,
  setSeat,
  type Member,
  type MemberKind,
  type Playbook,
  type Project,
  type Role,
  type State,
  type Task,
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
  state,
  refresh,
}: {
  project: Project;
  state: State;
  refresh: () => Promise<void>;
}) {
  const playbook = project.playbook;
  return (
    <div className="tab-stack">
      <section className="tab-panel card" aria-label="Team">
        <h2>Team</h2>
        {playbook ? (
          <>
            <Roles
              project={project}
              playbook={playbook}
              state={state}
              refresh={refresh}
            />
            {isCode(playbook) && (
              <QABrowser
                project={project}
                playbook={playbook}
                refresh={refresh}
              />
            )}
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
 * Each role on the team: who fills it, and its seats. A seat works on one
 * step at a time, so another seat for a role lets more of its work run at
 * once: Claudius gains Claudius #2. Members are added to and removed from
 * the owner's Team; here they are only given a role.
 */
function Roles({
  project,
  playbook,
  state,
  refresh,
}: {
  project: Project;
  playbook: Playbook;
  state: State;
  refresh: () => Promise<void>;
}) {
  const code = isCode(playbook);
  const kinds = seatKinds(code);
  const [shown, setShown] = useState<MemberKind | "all">("all");
  const { busy, error, run } = useAction();
  const act = (change: () => Promise<unknown>) =>
    void run(async () => {
      await change();
      await refresh();
    });
  return (
    <>
      <p className="hint">
        Each seat works on one step at a time, so more seats for a role let more
        of its work run at once.
      </p>
      <div className="segmented role-filter" role="group" aria-label="Show">
        {(["all", ...kinds] as const).map((kind) => (
          <button
            key={kind}
            type="button"
            aria-pressed={shown === kind}
            onClick={() => setShown(kind)}
          >
            {kind === "all" ? "All roles" : seatLabel(kind, code)}
          </button>
        ))}
      </div>
      <ul className="seats rows" aria-label="Roles">
        {kinds
          .filter((kind) => shown === "all" || shown === kind)
          .map((kind) => (
            <RoleGroup
              key={kind}
              kind={kind}
              label={seatLabel(kind, code)}
              project={project}
              playbook={playbook}
              state={state}
              busy={busy}
              onFill={(id) => act(() => setSeat(project.id, kind, id))}
              onAdd={(seat) => act(() => addSeat(project.id, seat))}
              onRemove={(seat) => act(() => removeSeat(project.id, seat))}
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
 * The request a seat is on now: the one its turn runs on, or else one whose
 * step it has taken; none while it is idle or keeping the to-do list.
 */
function seatTask(role: Role, tasks: Task[], state: State) {
  const turn = state.turns.find(
    (t) => t.seat === role.name && tasks.some((task) => task.id === t.task_id),
  );
  return (
    tasks.find((t) => t.id === turn?.task_id) ??
    tasks.find(
      (t) =>
        !finished(t) &&
        !!t.claims?.some((c) => c.seat === role.name && !c.held),
    )
  );
}

/** Seats filled alike: holding the same roles. */
const alike = (a: Role, b: Role) =>
  a.kinds.length === b.kinds.length &&
  a.kinds.every((kind) => b.kinds.includes(kind));

/**
 * One role: the member in it, the template's seat, or no one, with every
 * seat that holds it. Only research can be left out of a team that has it,
 * and no template has a designer or a PM. The PM keeps one to-do list, so
 * it has one seat.
 */
function RoleGroup({
  kind,
  label,
  project,
  playbook,
  state,
  busy,
  onFill,
  onAdd,
  onRemove,
}: {
  kind: MemberKind;
  label: string;
  project: Project;
  playbook: Playbook;
  state: State;
  busy: boolean;
  onFill: (id: string) => void;
  onAdd: (seat: string) => void;
  onRemove: (seat: string) => void;
}) {
  const members = state.members;
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
  const seats = playbook.roles.filter((r) => r.kinds.includes(kind));
  const last = seats.at(-1);
  const removable = !!last && seats.some((o) => o !== last && alike(o, last));
  return (
    <li className="seat role-group">
      <span className="seat-role">{label}</span>
      <span className="seat-who">
        {!filled && role && (
          <span className="muted">
            Template default · {engineLabel(role.engine)}
          </span>
        )}
        {!role && (
          <span className="muted">{optional ? "No research" : empty}</span>
        )}
        {kept && (
          <span className="muted small">
            Kept here; no longer holds this role on your team
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
        {last && kind !== "pm" && (
          <span
            className="seat-count"
            role="group"
            aria-label={`How many ${label} seats`}
          >
            <button
              type="button"
              className="btn btn-sm"
              aria-label={`One fewer ${label} seat`}
              disabled={busy || !removable}
              onClick={() => onRemove(last.name)}
            >
              −
            </button>
            <span aria-live="polite">
              {seats.length} {seats.length === 1 ? "seat" : "seats"}
            </span>
            <button
              type="button"
              className="btn btn-sm"
              aria-label={`One more ${label} seat`}
              disabled={busy}
              onClick={() => onAdd(last.name)}
            >
              +
            </button>
          </span>
        )}
      </span>
      {seats.length > 0 && (
        <SeatList label={label} seats={seats} project={project} state={state} />
      )}
    </li>
  );
}

/** A role's seats, each with the request it is on now and where. */
function SeatList({
  label,
  seats,
  project,
  state,
}: {
  label: string;
  seats: Role[];
  project: Project;
  state: State;
}) {
  const tasks = projectTasks(project, state.tasks);
  const lanes = boardColumns(project, tasks).flatMap((c) => c.lanes);
  const where = (task: Task) =>
    task.stage === "ready"
      ? readyLabel(project)
      : (lanes.find((l) => l.stage === task.stage)?.label ?? "");
  return (
    <ul className="seat-copies" aria-label={`${label} seats`}>
      {seats.map((role) => {
        const on = seatTask(role, tasks, state);
        return (
          <li key={role.name} className="seat-copy">
            <span className="seat-copy-name">
              <RoleName role={role} members={state.members} />
            </span>
            <span className="seat-copy-note muted small">
              {seatSummary(role)}
              {on && (
                <>
                  {" · "}
                  <a href={requestHref(project.id, on.id)}>
                    On {on.ref || on.objective}
                    {where(on) && ` · ${where(on)}`}
                  </a>
                </>
              )}
            </span>
          </li>
        );
      })}
    </ul>
  );
}

/**
 * "Claude", or "Researcher and implementer · Claude" for a seat that holds
 * more than the role it is listed under.
 */
function seatSummary(role: Role) {
  return role.kinds.length > 1
    ? [kindsLabel(role.kinds), engineLabel(role.engine)].join(" · ")
    : engineLabel(role.engine);
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

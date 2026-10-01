import { NoPM } from "./PMChat";
import { pmSeat } from "./members";
import { ProviderIcon } from "./ProviderIcon";
import { useState } from "react";
import { href, memberHref, projectHref, requestHref } from "./router";
import { isCode } from "./landing";
import { boardColumns, boardRows } from "./boardLanes";
import { finished, projectTasks } from "./stages";
import { holds, kindLabel, kindsLabel, memberOf } from "./members";
import { Avatar } from "./Avatar";
import { QABrowser } from "./QABrowser";
import { ErrorNotice, useAction } from "./ui";
import {
  addToRole,
  removeFromRole,
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
      {!pmSeat(project) && <NoPM project={project} />}
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
 * Each role on the team and the people in it. The owner adds people to a
 * role until there are enough, and removes them, seat by seat. A seat works
 * on one step at a time, so more seats for a role let more of its work run
 * at once. Members are added to and removed from the owner's Team; here they
 * are only given roles.
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
        Each seat works on one step at a time: add people to a role to run more
        of its work at once. Different people in a checking role each check
        every draft; more seats for one person share the checking.
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
              onAdd={(member) => act(() => addToRole(project.id, kind, member))}
              onRemove={(seat) =>
                act(() => removeFromRole(project.id, seat, kind))
              }
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

/** The option that adds the template's seat rather than a member. */
const templateSeat = "template";

/**
 * One role and everyone in it, each removable, with a way to add someone:
 * a member who holds the role, one already in it for another seat, or the
 * template's seat where the template has one. A team keeps an implementer
 * and a reviewer, and one PM at most.
 */
function RoleGroup({
  kind,
  label,
  project,
  playbook,
  state,
  busy,
  onAdd,
  onRemove,
}: {
  kind: MemberKind;
  label: string;
  project: Project;
  playbook: Playbook;
  state: State;
  busy: boolean;
  onAdd: (member: string) => void;
  onRemove: (seat: string) => void;
}) {
  const members = state.members;
  const seats = playbook.roles.filter((r) => r.kinds.includes(kind));
  const eligible = members.filter((m) => holds(m, kind));
  const templated = kind !== "designer" && kind !== "pm";
  const required = kind === "implementer" || kind === "reviewer";
  const addable = kind !== "pm" || seats.length === 0;
  const empty =
    kind === "researcher" ? "No research" : (nobody[kind] ?? "No one yet");
  const tasks = projectTasks(project, state.tasks);
  const lanes = [
    ...boardColumns(project, tasks).flatMap((c) => c.lanes),
    ...boardRows(project, tasks),
  ];
  const where = (task: Task) =>
    lanes.find((l) => l.stage === task.stage)?.label ?? "";
  const lastRequired = required && seats.length === 1;
  return (
    <li className="seat role-group">
      <span className="seat-role">{label}</span>
      <div className="role-body">
        {seats.length === 0 ? (
          <p className="muted small role-empty">{empty}</p>
        ) : (
          <ul className="seat-copies" aria-label={`${label} seats`}>
            {seats.map((role) => {
              const member = memberOf(role, members);
              const on = seatTask(role, tasks, state);
              const notes = [
                role.member ? seatSummary(role) : "Template default",
                role.member && !member ? "No longer on your team" : "",
                member && !holds(member, kind)
                  ? "Kept here; no longer holds this role on your team"
                  : "",
              ].filter(Boolean);
              return (
                <li key={role.name} className="seat-copy">
                  <span className="seat-copy-name">
                    <RoleName role={role} members={members} />
                    <ProviderIcon engine={role.engine} />
                  </span>
                  {(notes.length > 0 || on) && (
                    <span className="seat-copy-note muted small">
                      {notes.join(" · ")}
                      {on && (
                        <>
                          {notes.length > 0 && " · "}
                          <a href={requestHref(project.id, on.id)}>
                            On {on.ref || on.objective}
                            {where(on) && ` · ${where(on)}`}
                          </a>
                        </>
                      )}
                    </span>
                  )}
                  <button
                    type="button"
                    className="btn btn-quiet btn-sm seat-remove"
                    aria-label={`Remove ${role.name} from ${label}`}
                    title={
                      lastRequired
                        ? `A team needs a ${label.toLowerCase()}; add another first`
                        : `Remove ${role.name} from ${label}`
                    }
                    disabled={busy || lastRequired}
                    onClick={() => onRemove(role.name)}
                  >
                    ×
                  </button>
                </li>
              );
            })}
          </ul>
        )}
        {addable && (templated || eligible.length > 0) && (
          <select
            className="field role-add"
            aria-label={`Add to ${label}`}
            value=""
            disabled={busy}
            onChange={(e) =>
              onAdd(e.target.value === templateSeat ? "" : e.target.value)
            }
          >
            <option value="">+ Add someone…</option>
            {templated && (
              <option value={templateSeat}>Template default</option>
            )}
            {eligible.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name}
                {seats.some((r) => r.member === m.id) ? " (another seat)" : ""}
              </option>
            ))}
          </select>
        )}
      </div>
    </li>
  );
}

/** The extra kinds a seat holds alongside its listed role. */
function seatSummary(role: Role) {
  return role.kinds.length > 1 ? kindsLabel(role.kinds) : "";
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

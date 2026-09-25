import { useState, type FormEvent } from "react";
import { FileSystemPicker } from "./FileSystemPicker";
import { Folders } from "./ProjectFolders";
import { memberHref } from "./router";
import { isCode } from "./stages";
import { engineLabel, engines, holds, kindsLabel, memberOf } from "./members";
import { Avatar } from "./Avatar";
import { ErrorNotice, useAction } from "./ui";
import {
  setTeam,
  type Member,
  type MemberKind,
  type Playbook,
  type Project,
  type Role,
} from "./api";

const signing: Record<string, string> = {
  "": "Signed as your git config says",
  always: "Always signed",
  never: "Never signed",
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
              <TeamView playbook={playbook} members={members} />
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

function TeamView({
  playbook,
  members,
}: {
  playbook: Playbook;
  members: Member[];
}) {
  const code = isCode(playbook);
  return (
    <dl className="facts">
      {playbook.roles.map((role) => (
        <div key={role.name} className="fact-row">
          <dt>
            <RoleName role={role} members={members} />
          </dt>
          <dd>{seatSummary(role)}</dd>
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

/** "Planner and implementer · Claude", leaving out kinds the name says. */
function seatSummary(role: Role) {
  const kinds = kindsLabel(role.kinds);
  const named = kinds.toLowerCase() === role.name.toLowerCase();
  return [named ? "" : kinds, engineLabel(role.engine)]
    .filter(Boolean)
    .join(" · ");
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

const firstEngine = (
  roles: Role[] | undefined,
  kind: string,
  fallback: string,
) => roles?.find((r) => holds(r, kind))?.engine ?? fallback;

/** The member a slot was filled with, while that member is still on the team. */
function chosenMember(
  roles: Role[] | undefined,
  kind: MemberKind,
  members: Member[],
) {
  const id = roles?.find((r) => holds(r, kind))?.member;
  return members.some((m) => m.id === id && holds(m, kind)) ? (id ?? "") : "";
}

/** Sent as the planner to leave planning out of a code team. */
const noPlanning = "none";

/**
 * Who plans for a code team: the member in the seat that plans, the
 * template's planner, or no one.
 */
function chosenPlanner(playbook: Playbook | undefined, members: Member[]) {
  if (playbook?.template !== "code") return "";
  if (!playbook.roles.some((r) => holds(r, "planner"))) return noPlanning;
  return chosenMember(playbook.roles, "planner", members);
}

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
  const [who, setWho] = useState<Record<MemberKind, string>>(() => ({
    planner: chosenPlanner(playbook, members),
    implementer: chosenMember(playbook?.roles, "implementer", members),
    reviewer: chosenMember(playbook?.roles, "reviewer", members),
    qa: chosenMember(playbook?.roles, "qa", members),
    pm: chosenMember(playbook?.roles, "pm", members),
  }));
  const choose = (kind: MemberKind) => (id: string) =>
    setWho((current) => ({ ...current, [kind]: id }));
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
        implementer_member: who.implementer,
        reviewer_member: who.reviewer,
        qa_member: code ? who.qa : "",
        planner_member: code ? who.planner : "",
        pm_member: who.pm,
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
          {code && (
            <TeamSlot
              kind="planner"
              label="Planner"
              members={members}
              who={who.planner}
              onWho={choose("planner")}
              none="No planning"
            />
          )}
          <TeamSlot
            kind="implementer"
            label={code ? "Implementer" : "Writer"}
            members={members}
            who={who.implementer}
            onWho={choose("implementer")}
            engine={{ id: "team-writer", value: writer, onChange: setWriter }}
          />
          <TeamSlot
            kind="reviewer"
            label="Reviewer"
            members={members}
            who={who.reviewer}
            onWho={choose("reviewer")}
            engine={{
              id: "team-reviewer",
              value: reviewer,
              onChange: setReviewer,
            }}
          />
          {code && (
            <TeamSlot
              kind="qa"
              label="QA"
              members={members}
              who={who.qa}
              onWho={choose("qa")}
            />
          )}
          <TeamSlot
            kind="pm"
            label="PM"
            members={members}
            who={who.pm}
            onWho={choose("pm")}
            empty="No PM"
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

/**
 * One place on the team: who fills it, and the engine it runs on. A member
 * brings its own engine, so the engine is asked only of the template's role.
 * A place the team can do without offers leaving it out, as `none`; one the
 * template doesn't have says what having no one there means, as `empty`.
 */
function TeamSlot({
  kind,
  label,
  members,
  who,
  onWho,
  engine,
  none,
  empty,
}: {
  kind: MemberKind;
  label: string;
  members: Member[];
  who: string;
  onWho: (id: string) => void;
  engine?: { id: string; value: string; onChange: (value: string) => void };
  none?: string;
  empty?: string;
}) {
  const options = members.filter((m) => holds(m, kind));
  if (!options.length && !engine && !none) return null;
  return (
    <fieldset className="team-slot">
      <legend>{label}</legend>
      {(options.length > 0 || none) && (
        <label htmlFor={`team-${kind}-member`}>
          Who
          <select
            id={`team-${kind}-member`}
            className="field"
            value={who}
            onChange={(e) => onWho(e.target.value)}
          >
            <option value="">{empty ?? "Template default"}</option>
            {none && <option value={noPlanning}>{none}</option>}
            {options.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name}
              </option>
            ))}
          </select>
        </label>
      )}
      {engine && !who && (
        <EngineSelect
          id={engine.id}
          label="Engine"
          value={engine.value}
          onChange={engine.onChange}
        />
      )}
    </fieldset>
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

import { useEffect, useState, type FormEvent } from "react";
import { FileSystemPicker } from "./FileSystemPicker";
import { isCode } from "./landing";
import {
  engineOptions,
  unavailableForRoles,
  useEngineChoices,
} from "./engines";
import { teamChoice, teamWith } from "./members";
import { projectKind } from "./stages";
import { ErrorNotice, useAction } from "./ui";
import {
  bundledSkills,
  setBundledSkill,
  setTeam,
  type BundledSkill,
  type Member,
  type Playbook,
  type Project,
} from "./api";

/**
 * The kind of team and how it works. Who fills each role is chosen on the
 * Team tab, and saving here keeps them as they are.
 */
export function TeamSettings({
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
  if (editing)
    return (
      <section className="tab-panel card" aria-label="Team settings">
        <TeamEditor
          project={project}
          members={members}
          onDone={() => setEditing(false)}
          refresh={refresh}
        />
      </section>
    );
  return (
    <section className="tab-panel card" aria-label="Team settings">
      <div className="panel-head">
        <h3>Team settings</h3>
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
          <TeamFacts playbook={playbook} />
          <BundledSkillSettings project={project} refresh={refresh} />
        </>
      ) : (
        <p className="muted">No team yet, so nothing can be asked for.</p>
      )}
    </section>
  );
}

function BundledSkillSettings({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  const [catalog, setCatalog] = useState<BundledSkill[]>([]);
  const [loadError, setLoadError] = useState("");
  const { busy, error, run } = useAction();
  useEffect(() => {
    let active = true;
    bundledSkills()
      .then((skills) => {
        if (active) setCatalog(skills);
      })
      .catch((err: unknown) => {
        if (active)
          setLoadError(err instanceof Error ? err.message : String(err));
      });
    return () => {
      active = false;
    };
  }, []);
  return (
    <fieldset className="form" disabled={busy}>
      <legend>Bundled skills</legend>
      <p className="muted">
        Guidance supplied with the app for each role. Changes apply to new
        requests; existing requests keep their settings until they adopt the
        current team.
      </p>
      {catalog.map((skill) => (
        <label key={skill.name}>
          <input
            type="checkbox"
            checked={
              !project.playbook?.disabled_bundled_skills?.includes(skill.name)
            }
            onChange={(event) => {
              const enabled = event.target.checked;
              void run(async () => {
                await setBundledSkill(project.id, skill.name, enabled);
                await refresh();
              });
            }}
          />
          {skill.name} ({skill.role})
          <span className="muted"> {skill.description}</span>
        </label>
      ))}
      <ErrorNotice error={loadError || error} />
    </fieldset>
  );
}

function TeamFacts({ playbook }: { playbook: Playbook }) {
  const code = isCode(playbook);
  return (
    <dl className="facts">
      <div className="fact-row">
        <dt>Kind of work</dt>
        <dd>{projectKind(playbook)}</dd>
      </div>
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

/**
 * A team is saved whole, so who fills each role and where a code team works
 * are sent again as they are.
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
  const [loopback, setLoopback] = useState(playbook?.check_loopback === true);
  const [writer, setWriter] = useState(current?.writer_engine || "claude");
  const [reviewer, setReviewer] = useState(current?.reviewer_engine || "codex");
  const [rounds, setRounds] = useState(String(playbook?.max_rounds ?? 3));
  const [deliverTo, setDeliverTo] = useState(playbook?.deliver_to ?? "");
  const [picking, setPicking] = useState(false);
  const { busy, error, run } = useAction();
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      const team = teamWith(current ?? {}, {
        template,
        writer_engine: writer,
        reviewer_engine: reviewer,
        max_rounds: rounds,
        check: check.trim(),
        deliver_to: deliverTo,
      });
      await setTeam(
        project.id,
        code ? { ...team, check_loopback: loopback ? "yes" : "no" } : team,
      );
      await refresh();
      onDone();
    });
  }
  return (
    <>
      <form className="form" onSubmit={save} aria-label="Team settings">
        <h3>Team settings</h3>
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
          <>
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
            <label className="check">
              <input
                type="checkbox"
                checked={loopback}
                onChange={(e) => setLoopback(e.target.checked)}
                aria-describedby="team-loopback-hint"
              />
              <span>Let the check use this machine's own network</span>
            </label>
            <p className="hint" id="team-loopback-hint">
              For tests that start a local server. The project's check runs in a
              sandbox crew-assistant hosts, for the implementer and QA on any
              engine; nothing beyond this machine is reachable.
            </p>
          </>
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
  const choices = useEngineChoices();
  const cliChoices = choices.filter((choice) => choice.cli);
  const offered = engineOptions(
    cliChoices,
    "roles",
    value === "openai-compatible" ? "" : value,
  );
  // The engines a role can't run on yet are shown, and why.
  const unavailable = unavailableForRoles(cliChoices, offered);
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
          aria-describedby={
            unavailable.length ? `${id}-unavailable` : undefined
          }
        >
          {offered.map((engine) => (
            <option key={engine.id} value={engine.id}>
              {engine.label}
            </option>
          ))}
          {unavailable.map((item) => (
            <option key={item.id} value={item.id} disabled>
              {item.label} (unavailable here)
            </option>
          ))}
        </select>
      </label>
      {unavailable.length > 0 && (
        <p className="hint" id={`${id}-unavailable`}>
          {unavailable.map((item) => item.reason).join(" ")}
        </p>
      )}
    </fieldset>
  );
}

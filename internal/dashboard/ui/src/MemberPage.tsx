import { useState, type FormEvent } from "react";
import { Avatar } from "./Avatar";
import { MemberForm } from "./MemberForm";
import { DrawingStatus, LookForm } from "./Redraw";
import { href, projectHref } from "./router";
import {
  learnedBy,
  learningParts,
  memberProjects,
  memberSummary,
} from "./members";
import { ErrorNotice, fullDateLabel, sinceLabel, useAction } from "./ui";
import {
  addLearning,
  deleteMember,
  forgetLearning,
  redrawMember,
  type Learning,
  type Member,
  type State,
} from "./api";

export function MemberPage({
  member,
  state,
  refresh,
}: {
  member: Member;
  state: State;
  refresh: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const [redrawing, setRedrawing] = useState(false);
  const projects = memberProjects(member, state.projects);
  return (
    <div className="page team">
      <header className="page-header">
        <nav className="crumbs" aria-label="Breadcrumb">
          <a href={href({ page: "team" })}>Team</a>
          <span aria-hidden="true">/</span>
          <span aria-current="page">{member.name}</span>
        </nav>
        {editing ? (
          <section className="card team-form">
            <MemberForm
              member={member}
              onCancel={() => setEditing(false)}
              onSaved={async () => {
                await refresh();
                setEditing(false);
              }}
            />
          </section>
        ) : (
          <div className="member-head">
            <Avatar of={member} size={64} />
            <div className="member-head-text">
              <h1>{member.name}</h1>
              <p className="soft">{memberSummary(member)}</p>
            </div>
            <div className="actions">
              {!member.drawing && !redrawing && (
                <button
                  type="button"
                  className="btn btn-quiet btn-sm"
                  onClick={() => setRedrawing(true)}
                >
                  Redraw
                </button>
              )}
              <button
                type="button"
                className="btn btn-sm"
                onClick={() => setEditing(true)}
              >
                Edit
              </button>
            </div>
          </div>
        )}
        {!editing &&
          (redrawing && !member.drawing ? (
            <section className="card team-form">
              <LookForm
                id="member-look"
                face={member}
                onCancel={() => setRedrawing(false)}
                onRedraw={async (look) => {
                  await redrawMember(member.id, look);
                  await refresh();
                  setRedrawing(false);
                }}
              />
            </section>
          ) : (
            <DrawingStatus face={member} />
          ))}
      </header>
      {projects.length > 0 && (
        <section className="section" aria-label="Projects">
          <div className="section-title">
            <h2>Projects</h2>
          </div>
          <ul className="card rows">
            {projects.map((p) => (
              <li key={p.id}>
                <a className="member-project" href={projectHref(p.id, "team")}>
                  {p.title}
                </a>
              </li>
            ))}
          </ul>
        </section>
      )}
      <Learnings member={member} state={state} refresh={refresh} />
      <DeleteMember member={member} refresh={refresh} />
    </div>
  );
}

const newestFirst = (a: Learning, b: Learning) =>
  (b.at ?? "").localeCompare(a.at ?? "");

const foldAt = 240;

function Learnings({
  member,
  state,
  refresh,
}: {
  member: Member;
  state: State;
  refresh: () => Promise<void>;
}) {
  const [when, setWhen] = useState("");
  const [text, setText] = useState("");
  const [projectId, setProjectId] = useState("");
  const [forgetting, setForgetting] = useState("");
  const adding = useAction();
  const forget = useAction();
  const learnings = [...member.learnings].sort(newestFirst);
  const projectTitle = (id?: string) =>
    state.projects.find((p) => p.id === id)?.title;
  async function add(e: FormEvent) {
    e.preventDefault();
    await adding.run(async () => {
      await addLearning(member.id, {
        when: when.trim(),
        text: text.trim(),
        project_id: projectId,
      });
      setWhen("");
      setText("");
      setProjectId("");
      await refresh();
    });
  }
  async function remove(id: string) {
    setForgetting(id);
    await forget.run(async () => {
      await forgetLearning(member.id, id);
      await refresh();
    });
    setForgetting("");
  }
  return (
    <section className="section" aria-label="Learnings">
      <div className="section-title">
        <h2>Learnings</h2>
        {learnings.length > 0 && (
          <span className="count">{learnings.length}</span>
        )}
      </div>
      <p className="hint">
        {member.name} starts each task knowing when each one applies, and reads
        it only then. It adds its own too, never about one project.
      </p>
      <ErrorNotice error={forget.error} />
      {learnings.length > 0 && (
        <ul className="card rows">
          {learnings.map((l) => (
            <LearningRow
              key={l.id}
              learning={l}
              by={learnedBy(
                l,
                member.name,
                state.assistant.name || "Assistant",
                projectTitle(l.project_id),
              )}
              busy={forgetting === l.id}
              onForget={() => void remove(l.id)}
            />
          ))}
        </ul>
      )}
      <form className="card form learning-add" onSubmit={add}>
        <label htmlFor="learning-when">
          When it applies
          <input
            id="learning-when"
            className="field"
            maxLength={160}
            placeholder="e.g. Reviewing error handling"
            value={when}
            onChange={(e) => setWhen(e.target.value)}
          />
          <span className="hint">Optional.</span>
        </label>
        <label htmlFor="learning-text">
          Learning
          <textarea
            id="learning-text"
            className="field"
            rows={3}
            maxLength={1500}
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
        </label>
        <div className="learning-add-foot">
          <label htmlFor="learning-project">
            Learned on
            <select
              id="learning-project"
              className="field"
              value={projectId}
              onChange={(e) => setProjectId(e.target.value)}
            >
              <option value="">No particular project</option>
              {state.projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.title}
                </option>
              ))}
            </select>
          </label>
          <button
            className="btn btn-primary"
            disabled={adding.busy || !text.trim()}
          >
            Add
          </button>
        </div>
        <ErrorNotice error={adding.error} />
      </form>
    </section>
  );
}

function LearningRow({
  learning,
  by,
  busy,
  onForget,
}: {
  learning: Learning;
  by: string;
  busy: boolean;
  onForget: () => void;
}) {
  const since = sinceLabel(learning.at);
  const { heading, body } = learningParts(learning);
  return (
    <li className="learning-row">
      <div className="learning-text">
        <p className="learning-when">{heading}</p>
        {body && <Folded text={body} />}
        <p className="muted small">
          {by}
          {since && (
            <>
              {" · "}
              <time dateTime={learning.at} title={fullDateLabel(learning.at)}>
                {since}
              </time>
            </>
          )}
        </p>
      </div>
      <button
        type="button"
        className="btn btn-quiet btn-sm"
        disabled={busy}
        onClick={onForget}
      >
        Forget
      </button>
    </li>
  );
}

/** Long text shows its start until the owner asks for the rest. */
function Folded({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  if (open || text.length <= foldAt)
    return <p className="learning-body">{text}</p>;
  return (
    <p className="learning-body">
      {text.slice(0, foldAt).replace(/\s+\S*$/, "")}…{" "}
      <button
        type="button"
        className="link-button small"
        onClick={() => setOpen(true)}
      >
        Show all
      </button>
    </p>
  );
}

function DeleteMember({
  member,
  refresh,
}: {
  member: Member;
  refresh: () => Promise<void>;
}) {
  const [confirming, setConfirming] = useState(false);
  const { busy, error, run } = useAction();
  async function remove() {
    await run(async () => {
      await deleteMember(member.id);
      window.location.hash = href({ page: "team" });
      await refresh();
    });
  }
  return (
    <section className="section member-delete" aria-label="Delete member">
      {confirming ? (
        <div className="actions">
          <button
            type="button"
            className="btn btn-danger btn-sm"
            disabled={busy}
            onClick={() => void remove()}
          >
            Delete {member.name}
          </button>
          <button
            type="button"
            className="btn btn-quiet btn-sm"
            disabled={busy}
            onClick={() => setConfirming(false)}
          >
            Keep
          </button>
          <span className="muted small">Project teams keep their copy.</span>
        </div>
      ) : (
        <div className="actions">
          <button
            type="button"
            className="btn btn-quiet btn-sm"
            onClick={() => setConfirming(true)}
          >
            Delete member
          </button>
        </div>
      )}
      <ErrorNotice error={error} />
    </section>
  );
}

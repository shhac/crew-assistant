import { useState, type FormEvent } from "react";
import { MemberForm } from "./MemberForm";
import { href, projectHref } from "./router";
import { memberProjects, memberSummary } from "./stages";
import {
  Avatar,
  ErrorNotice,
  fullDateLabel,
  sinceLabel,
  useAction,
} from "./ui";
import {
  addLearning,
  deleteMember,
  forgetLearning,
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
            <button
              type="button"
              className="btn btn-sm"
              onClick={() => setEditing(true)}
            >
              Edit
            </button>
          </div>
        )}
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

function Learnings({
  member,
  state,
  refresh,
}: {
  member: Member;
  state: State;
  refresh: () => Promise<void>;
}) {
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
      await addLearning(member.id, text.trim(), projectId);
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
        They go with {member.name} into every task it starts, in any project.
      </p>
      <ErrorNotice error={forget.error} />
      {learnings.length > 0 && (
        <ul className="card rows">
          {learnings.map((l) => (
            <LearningRow
              key={l.id}
              learning={l}
              project={projectTitle(l.project_id)}
              busy={forgetting === l.id}
              onForget={() => void remove(l.id)}
            />
          ))}
        </ul>
      )}
      <form className="card form learning-add" onSubmit={add}>
        <label htmlFor="learning-text">
          Add a learning
          <textarea
            id="learning-text"
            className="field"
            rows={2}
            maxLength={300}
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
  project,
  busy,
  onForget,
}: {
  learning: Learning;
  project?: string;
  busy: boolean;
  onForget: () => void;
}) {
  const when = sinceLabel(learning.at);
  return (
    <li className="learning-row">
      <div className="learning-text">
        <p>{learning.text}</p>
        {(project || when) && (
          <p className="muted small">
            {project}
            {project && when && " · "}
            {when && (
              <time dateTime={learning.at} title={fullDateLabel(learning.at)}>
                {when}
              </time>
            )}
          </p>
        )}
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

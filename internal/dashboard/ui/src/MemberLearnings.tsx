import { useState, type FormEvent } from "react";
import { ErrorNotice, fullDateLabel, sinceLabel, useAction } from "./ui";
import {
  addLearning,
  forgetLearning,
  type Learning,
  type Member,
  type State,
} from "./api";

/**
 * A learning is headed by when it applies. Older learnings had that as the
 * start of their text, so it is not said twice.
 */
export function learningParts(learning: Learning) {
  const text = learning.text.trim();
  const when = learning.when;
  if (!when) return firstSentenceParts(text);
  return { heading: when, body: afterPrefix(text, when) };
}

// A when that ends mid-word, like "Review" before "Reviewers …", is the
// owner's own heading rather than the text's start, so the text stays whole.
function afterPrefix(text: string, prefix: string) {
  if (!text.startsWith(prefix)) return text;
  const rest = text.slice(prefix.length);
  if (/^[\p{L}\p{N}]/u.test(rest)) return text;
  return rest.replace(/^[.!?:;,]?\s*/, "");
}

function firstSentenceParts(text: string) {
  const first = /^[\s\S]*?[.!?](?=\s|$)/.exec(text)?.[0] ?? text;
  return { heading: first, body: text.slice(first.length).trim() };
}

/** Who recorded a learning, and where. */
export function learnedBy(
  learning: Learning,
  member: string,
  assistant: string,
  project?: string,
) {
  const who = recorder(learning, member, assistant);
  return project ? `${who} on ${project}` : who;
}

function recorder(learning: Learning, member: string, assistant: string) {
  if (learning.source === "member") return `${member} learned this`;
  if (learning.source === "assistant") return `Added by ${assistant}`;
  return "You added this";
}

const newestFirst = (a: Learning, b: Learning) =>
  (b.at ?? "").localeCompare(a.at ?? "");

const foldAt = 240;

export function Learnings({
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

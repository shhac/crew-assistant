import { useLayoutEffect, useRef, useState, type FormEvent } from "react";
import {
  setRelease,
  type Playbook,
  type Project,
  type ReleasePolicy,
  type ReleaseRecord,
} from "./api";
import { ErrorNotice, useAction, fullDateLabel, recordedTime } from "./ui";

export function ReleasedMeta({ release }: { release?: ReleaseRecord }) {
  if (!release) return null;
  return (
    <>
      <span aria-hidden="true">·</span>
      <span>
        Released {release.version} ·{" "}
        {recordedTime(release.at)?.toLocaleDateString(undefined, {
          dateStyle: "medium",
        })}
        {!release.published && " (local only)"}
      </span>
    </>
  );
}

function RecentRelease({ release }: { release: ReleaseRecord }) {
  const [expanded, setExpanded] = useState(false);
  const [long, setLong] = useState(false);
  const notes = useRef<HTMLParagraphElement>(null);
  useLayoutEffect(() => {
    if (expanded || !notes.current) return;
    const node = notes.current;
    const measure = () => setLong(node.scrollHeight > node.clientHeight);
    measure();
    const observer =
      typeof ResizeObserver === "undefined"
        ? undefined
        : new ResizeObserver(measure);
    observer?.observe(node);
    window.addEventListener("resize", measure);
    return () => {
      observer?.disconnect();
      window.removeEventListener("resize", measure);
    };
  }, [expanded, release.notes]);
  return (
    <li>
      <p>
        {release.version} · {fullDateLabel(release.at)}
        {release.approved_by &&
          ` · ${release.approved_by === "pm" ? "PM" : "You"} approved`}
      </p>
      <p
        ref={notes}
        className={
          expanded ? "release-notes" : "release-notes release-notes-clamped"
        }
      >
        {release.notes}
      </p>
      {long && (
        <button
          type="button"
          className="btn btn-quiet btn-sm"
          onClick={() => setExpanded(!expanded)}
        >
          {expanded ? "Show less" : "Show more"}
        </button>
      )}
      <p className="muted small">
        {release.published
          ? `Published to ${release.published}`
          : `Local only · ${release.note || "tag not published"}`}
      </p>
    </li>
  );
}

export function ReleaseSettings({
  project,
  playbook,
  refresh,
}: {
  project: Project;
  playbook: Playbook;
  refresh: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const policy = playbook.release;
  const push = !playbook.land?.pull_requests && playbook.land?.via === "push";
  if (editing)
    return (
      <section className="tab-panel card" aria-label="Releases">
        <ReleaseEditor
          project={project}
          policy={policy}
          push={push}
          refresh={refresh}
          onDone={() => setEditing(false)}
        />
      </section>
    );
  return (
    <section className="tab-panel card" aria-label="Releases">
      <div className="panel-head">
        <h3>Releases</h3>
        <button
          type="button"
          className="btn btn-sm"
          onClick={() => setEditing(true)}
        >
          {policy ? "Edit" : "Set up releases"}
        </button>
      </div>
      {policy ? (
        <>
          <dl className="facts">
            <div className="fact-row">
              <dt>When to release</dt>
              <dd>{policy.when}</dd>
            </div>
            <div className="fact-row">
              <dt>Check before release</dt>
              <dd>
                {policy.check ? (
                  <code>{policy.check}</code>
                ) : (
                  "No release check"
                )}
              </dd>
            </div>
            {push && policy.github && (
              <div className="fact-row">
                <dt>GitHub repository</dt>
                <dd>
                  <code>{policy.github}</code>
                </dd>
              </div>
            )}
            <div className="fact-row">
              <dt>Approval</dt>
              <dd>
                {policy.approve === "pm"
                  ? "The PM approves each release"
                  : "You approve each release"}
              </dd>
            </div>
          </dl>
          <p className="muted small">
            A release checks the landed commit in QA’s sandbox, then tags that
            checked commit. The tag push starts the project’s CI.
          </p>
          {push && !policy.github && (
            <p className="muted small">
              Tags stay in this repository until you push them yourself.
            </p>
          )}
        </>
      ) : (
        <p className="muted">
          This project never releases. Set when the PM should propose a version;
          it will publish an annotated git tag after any release check passes.
        </p>
      )}
      {!!project.releases?.length && (
        <>
          <h4>Recent releases</h4>
          <ul>
            {project.releases.map((r) => (
              <RecentRelease key={r.version} release={r} />
            ))}
          </ul>
        </>
      )}
    </section>
  );
}

function ReleaseEditor({
  project,
  policy,
  push,
  refresh,
  onDone,
}: {
  project: Project;
  policy?: ReleasePolicy;
  push: boolean;
  refresh: () => Promise<void>;
  onDone: () => void;
}) {
  const [value, setValue] = useState<ReleasePolicy>({
    when: "",
    check: "",
    github: "",
    approve: "",
    ...policy,
  });
  const { busy, error, run } = useAction();
  const field =
    (key: keyof ReleasePolicy) => (e: { target: { value: string } }) =>
      setValue((v) => ({ ...v, [key]: e.target.value }));
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await setRelease(project.id, {
        when: value.when.trim(),
        check: value.check?.trim() || "",
        github: push ? value.github?.trim() || "" : "",
        approve: value.approve || "",
      });
      await refresh();
      onDone();
    });
  }
  async function remove() {
    await run(async () => {
      await setRelease(project.id, null);
      await refresh();
      onDone();
    });
  }
  return (
    <form className="form" aria-label="Releases" onSubmit={save}>
      <h3>Releases</h3>
      <p className="hint">
        The PM uses this guidance when it looks after a landing. A release is an
        annotated tag of the checked landed commit; publishing beyond a tag push
        belongs in CI.
      </p>
      <label htmlFor="release-when">
        When to release
        <textarea
          id="release-when"
          className="field"
          rows={3}
          maxLength={1000}
          required
          value={value.when}
          placeholder="After a user-visible feature lands, at most once a day."
          onChange={field("when")}
        />
        <span className="hint">
          Plain-language guidance for the PM. Saving this turns releases on.
        </span>
      </label>
      <label htmlFor="release-check">
        How to check a release
        <input
          id="release-check"
          className="field"
          maxLength={500}
          value={value.check || ""}
          placeholder="make release-check VERSION={version}"
          onChange={field("check")}
        />
        <span className="hint">
          Runs once in QA’s sandbox on a read-only checkout of the landed
          commit. It must only verify; it cannot publish. Use {"{version}"} for
          the proposed version.
        </span>
      </label>
      {push && (
        <label htmlFor="release-github">
          GitHub repository
          <input
            id="release-github"
            className="field"
            value={value.github || ""}
            placeholder="owner/name"
            onChange={field("github")}
          />
          <span className="hint">
            Before pushing the tag, the daemon fast-forwards the target branch
            to the checked commit. Leave blank to keep tags local.
          </span>
        </label>
      )}
      <label htmlFor="release-approve">
        Who approves
        <select
          id="release-approve"
          className="field"
          value={value.approve || ""}
          onChange={field("approve")}
        >
          <option value="">You approve each release</option>
          <option value="pm">The PM approves each release</option>
        </select>
        {value.approve === "pm" && (
          <span className="hint">
            The PM’s proposal proceeds after its check passes.
          </span>
        )}
      </label>
      {policy && (
        <p className="hint">
          Removing this stops future release proposals. A check or publishing
          step already under way finishes.
        </p>
      )}
      <ErrorNotice error={error} />
      <div className="actions">
        <button type="submit" className="btn btn-primary" disabled={busy}>
          Save
        </button>
        {policy && (
          <button
            type="button"
            className="btn btn-quiet"
            disabled={busy}
            onClick={() => void remove()}
          >
            Remove releases
          </button>
        )}
        <button
          type="button"
          className="btn btn-quiet"
          disabled={busy}
          onClick={onDone}
        >
          Cancel
        </button>
      </div>
    </form>
  );
}

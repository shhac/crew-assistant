import { useEffect, useState } from "react";
import { ConversationMarkdown } from "./ConversationMarkdown";
import { isCode, roleName, taskPlaybook, verdictOutcome } from "./stages";
import { ErrorNotice, Pill, dateLabel } from "./ui";
import {
  errorText,
  revisionFiles,
  type Project,
  type Revision,
  type RevisionFile,
  type Task,
  type Verdict,
} from "./api";

/** Each draft, newest first, with what every checker said about it. */
export function Drafts({
  project,
  task,
  collapsed,
}: {
  project: Project;
  task: Task;
  /** The decision above already shows the latest checks. */
  collapsed: boolean;
}) {
  const revisions = [...(task.revisions ?? [])].reverse();
  if (!revisions.length) return null;
  const code = isCode(taskPlaybook(task, project));
  return (
    <section className="section" aria-label="Drafts">
      <h3>{code ? "Changes" : "Drafts"}</h3>
      {revisions.map((r, i) => (
        <details key={r.n} className="draft card" open={i === 0 && !collapsed}>
          <summary>
            <span className="draft-name">
              {code ? "Change" : "Draft"} {r.n}
            </span>
            <span className="draft-checks">
              {(task.verdicts ?? [])
                .filter((v) => v.revision === r.n)
                .map((v, j) => (
                  <Pill key={j} tone={verdictOutcome[v.outcome]?.tone}>
                    {v.role}: {verdictOutcome[v.outcome]?.label ?? v.outcome}
                  </Pill>
                ))}
            </span>
            {r.at && (
              <time className="muted small" dateTime={r.at}>
                {dateLabel(r.at)}
              </time>
            )}
          </summary>
          <DraftDetail
            project={project}
            task={task}
            revision={r}
            code={code}
            latest={i === 0}
          />
        </details>
      ))}
    </section>
  );
}

function DraftDetail({
  project,
  task,
  revision,
  code,
  latest,
}: {
  project: Project;
  task: Task;
  revision: Revision;
  code: boolean;
  latest: boolean;
}) {
  const verdicts = (task.verdicts ?? []).filter(
    (v) => v.revision === revision.n,
  );
  return (
    <div className="draft-detail">
      {revision.summary && (
        <p>
          {/* A clean catch-up merge is crew-assistant's own, not the team's. */}
          {!revision.clean_merge_of && (
            <>
              <strong>
                {roleName(task, "implementer", "Implementer")}
              </strong>{" "}
            </>
          )}
          {revision.summary}
        </p>
      )}
      {revision.brief_version !== project.brief.version && (
        <p className="muted small">
          Written for brief version {revision.brief_version}.
        </p>
      )}
      {verdicts.map((v, i) => (
        <VerdictView key={i} verdict={v} asked={!!v.asked} />
      ))}
      {code
        ? !!revision.files?.length && (
            <details className="disclosure">
              <summary>
                {revision.files.length} files
                {revision.ref && (
                  <>
                    {" "}
                    at <code>{revision.ref.slice(0, 7)}</code>
                  </>
                )}
              </summary>
              <ul className="file-list">
                {revision.files.map((f) => (
                  <li key={f}>
                    <code>{f}</code>
                  </li>
                ))}
              </ul>
            </details>
          )
        : latest && (
            <DraftFiles
              projectID={project.id}
              taskID={task.id}
              n={revision.n}
            />
          )}
    </div>
  );
}

function VerdictView({ verdict, asked }: { verdict: Verdict; asked: boolean }) {
  return (
    <div className="check-note">
      <p>
        <strong>{verdict.role}</strong>
        {asked && <span className="muted small"> (asked directly)</span>}{" "}
        {verdict.summary}
      </p>
      {verdict.question && <p className="soft">Question: {verdict.question}</p>}
      {!!verdict.findings?.length && (
        <ul className="findings">
          {verdict.findings.map((f, i) => (
            <li key={i}>
              {f.criterion && <span className="muted">{f.criterion}: </span>}
              {f.note}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

type Loaded =
  | { state: "loading" }
  | { state: "failed"; error: string }
  | { state: "ready"; files: RevisionFile[] };

/** A draft's files, read from where the team keeps them. */
export function DraftFiles({
  projectID,
  taskID,
  n,
}: {
  projectID: string;
  taskID: string;
  n: number;
}) {
  const [loaded, setLoaded] = useState<Loaded>({ state: "loading" });
  useEffect(() => {
    const controller = new AbortController();
    setLoaded({ state: "loading" });
    revisionFiles(projectID, taskID, n, controller.signal)
      .then((files) => setLoaded({ state: "ready", files }))
      .catch((error) => {
        if (!controller.signal.aborted)
          setLoaded({ state: "failed", error: errorText(error) });
      });
    return () => controller.abort();
  }, [projectID, taskID, n]);
  if (loaded.state === "loading")
    return <p className="muted small">Loading…</p>;
  if (loaded.state === "failed") return <ErrorNotice error={loaded.error} />;
  if (!loaded.files.length)
    return <p className="muted small">This draft has no files.</p>;
  return (
    <div className="draft-files">
      {loaded.files.map((file) => (
        <DraftFile key={file.path} file={file} />
      ))}
    </div>
  );
}

const isMarkdown = (path: string) => /\.(md|markdown|mdown)$/i.test(path);

function sizeLabel(bytes: number) {
  if (bytes < 1024) return `${bytes} bytes`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function DraftFile({ file }: { file: RevisionFile }) {
  return (
    <article className="draft-file">
      <h4 className="draft-file-name">
        <code>{file.path}</code>{" "}
        <span className="muted small">{sizeLabel(file.size)}</span>
      </h4>
      {file.binary ? (
        <p className="muted small">Not shown: it isn't text.</p>
      ) : isMarkdown(file.path) ? (
        <ConversationMarkdown content={file.content ?? ""} />
      ) : (
        <pre>{file.content}</pre>
      )}
      {file.truncated && (
        <p className="muted small">
          Showing the start. The whole file is {sizeLabel(file.size)}.
        </p>
      )}
    </article>
  );
}

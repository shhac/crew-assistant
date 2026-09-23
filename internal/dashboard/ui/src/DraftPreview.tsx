import { useEffect, useState } from "react";
import { ConversationMarkdown } from "./ConversationMarkdown";
import { ErrorNotice, Status } from "./ui";
import { taskStatusLine } from "./taskStatus";
import {
  errorText,
  revisionFiles,
  type Project,
  type RevisionFile,
  type Task,
} from "./api";

/** The newest draft of the most recent task that has one. */
export function LatestDeliverable({
  project,
  tasks,
}: {
  project: Project;
  tasks: Task[];
}) {
  const task = tasks.find((t) => t.revisions?.length);
  if (!task) return null;
  return <Deliverable key={task.id} project={project} task={task} />;
}

function Deliverable({ project, task }: { project: Project; task: Task }) {
  const drafts = (task.revisions ?? []).map((r) => r.n).reverse();
  const [chosen, setChosen] = useState<number | null>(null);
  const shown = chosen ?? drafts[0];
  const status = taskStatusLine(task);
  return (
    <section className="project-card" aria-label="Latest deliverable">
      <div className="section-heading">
        <h2>Latest deliverable</h2>
        <Status tone={status.tone}>{status.label}</Status>
      </div>
      <p className="deliverable-objective">{task.objective}</p>
      {drafts.length > 1 && (
        <label htmlFor="deliverable-draft" className="deliverable-picker">
          Draft
          <select
            id="deliverable-draft"
            value={shown}
            onChange={(e) => {
              const n = Number(e.target.value);
              setChosen(n === drafts[0] ? null : n);
            }}
          >
            {drafts.map((n, i) => (
              <option key={n} value={n}>
                Draft {n}
                {i === 0 ? " (latest)" : ""}
              </option>
            ))}
          </select>
        </label>
      )}
      <DraftFiles projectID={project.id} taskID={task.id} n={shown} />
    </section>
  );
}

type Loaded =
  | { state: "loading" }
  | { state: "failed"; error: string }
  | { state: "ready"; files: RevisionFile[] };

function DraftFiles({
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
    return <p className="muted">Opening draft {n}…</p>;
  if (loaded.state === "failed") return <ErrorNotice error={loaded.error} />;
  if (!loaded.files.length)
    return <p className="muted">Draft {n} has no files.</p>;
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
      <h3>
        {file.path} <span>{sizeLabel(file.size)}</span>
      </h3>
      {file.binary ? (
        <p className="muted">
          {file.path} is not text ({sizeLabel(file.size)}), so it isn’t shown
          here.
        </p>
      ) : isMarkdown(file.path) ? (
        <ConversationMarkdown content={file.content ?? ""} />
      ) : (
        <pre>{file.content}</pre>
      )}
      {file.truncated && (
        <p className="field-hint">
          Only the beginning of {file.path} is shown; the full file is{" "}
          {sizeLabel(file.size)}.
        </p>
      )}
    </article>
  );
}

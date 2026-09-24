import { useEffect, useState } from "react";
import { ConversationMarkdown } from "./ConversationMarkdown";
import { ErrorNotice } from "./ui";
import { errorText, revisionFiles, type RevisionFile } from "./api";

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

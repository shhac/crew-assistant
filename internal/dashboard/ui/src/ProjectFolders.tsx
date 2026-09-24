import { useState } from "react";
import { FileSystemPicker } from "./FileSystemPicker";
import { ErrorNotice, useAction } from "./ui";
import { setDirectories, type Project } from "./api";

export function directoryName(path: string) {
  return (
    path
      .replace(/[\\/]+$/, "")
      .split(/[\\/]/)
      .pop() || path
  );
}

export function DirectoryList({
  paths,
  onRemove,
}: {
  paths: string[];
  onRemove?: (path: string) => void;
}) {
  return (
    <ul className="folders">
      {paths.map((path) => (
        <li key={path}>
          <code title={path}>{path}</code>
          {onRemove && (
            <button
              type="button"
              className="btn btn-quiet btn-sm"
              aria-label={`Remove folder ${path}`}
              onClick={() => onRemove(path)}
            >
              Remove
            </button>
          )}
        </li>
      ))}
    </ul>
  );
}

export function Folders({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const [picking, setPicking] = useState(false);
  const [paths, setPaths] = useState(project.directories || []);
  const { busy, error, setError, run } = useAction();
  async function save() {
    await run(async () => {
      await setDirectories(project.id, paths);
      await refresh();
      setEditing(false);
    });
  }
  const shown = editing ? paths : project.directories || [];
  return (
    <section className="tab-panel card" aria-label="Folders">
      <div className="panel-head">
        <h2>Folders</h2>
        {!editing && (
          <button
            type="button"
            className="btn btn-sm"
            onClick={() => {
              setPaths(project.directories || []);
              setError("");
              setEditing(true);
            }}
          >
            {project.directories?.length ? "Edit" : "Add folders"}
          </button>
        )}
      </div>
      {shown.length ? (
        <DirectoryList
          paths={shown}
          onRemove={
            editing && !busy
              ? (path) =>
                  setPaths((previous) =>
                    previous.filter((value) => value !== path),
                  )
              : undefined
          }
        />
      ) : (
        <p className="muted">No folders.</p>
      )}
      {editing && (
        <div className="actions">
          <button
            type="button"
            className="btn"
            disabled={busy}
            onClick={() => setPicking(true)}
          >
            Add folders
          </button>
          <button
            type="button"
            className="btn btn-primary"
            disabled={busy}
            onClick={() => void save()}
          >
            Save folders
          </button>
          <button
            type="button"
            className="btn btn-quiet"
            disabled={busy}
            onClick={() => setEditing(false)}
          >
            Cancel
          </button>
        </div>
      )}
      <ErrorNotice error={error} />
      {project.scratch_directory && (
        <p className="muted small">
          The team's own files are in <code>{project.scratch_directory}</code>
        </p>
      )}
      {picking && (
        <FileSystemPicker
          kind="directory"
          multiple
          initialPath={paths[0]}
          onCancel={() => setPicking(false)}
          onSelect={(selection) => {
            setPaths((previous) => [...new Set([...previous, ...selection])]);
            setPicking(false);
          }}
        />
      )}
    </section>
  );
}

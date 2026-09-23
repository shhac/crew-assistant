import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, errorText, type Project } from "./api";
import { FileSystemPicker } from "./FileSystemPicker";

function directoryName(path: string) {
  return (
    path
      .replace(/[\\/]+$/, "")
      .split(/[\\/]/)
      .pop() || path
  );
}
function DirectoryList({
  paths,
  onRemove,
}: {
  paths: string[];
  onRemove?: (path: string) => void;
}) {
  return (
    <ul className="project-directories">
      {paths.map((path) => (
        <li key={path}>
          <span title={path}>{path}</span>
          {onRemove && (
            <button
              type="button"
              className="text-button"
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
export function NewProject({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => Promise<void>;
}) {
  const [mode, setMode] = useState<"existing" | "new">("existing");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [criteria, setCriteria] = useState("");
  const [directories, setDirectories] = useState<string[]>([]);
  const [picking, setPicking] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => element?.close();
  }, []);
  async function create(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api("/api/projects", {
        method: "POST",
        body: JSON.stringify({
          title: title.trim(),
          description: description.trim(),
          acceptance_criteria: criteria.trim(),
          directories: mode === "existing" ? directories : [],
        }),
      });
      await onCreated();
    } catch (error) {
      setError(errorText(error));
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <dialog
        ref={dialog}
        className="project-dialog"
        aria-labelledby="project-dialog-title"
        onCancel={(e) => {
          if (busy) e.preventDefault();
          else onClose();
        }}
      >
        <div className="dialog-header">
          <p className="eyebrow">BRING YOUR WORK TOGETHER</p>
          <button
            type="button"
            className="icon-button"
            aria-label="Close new project"
            disabled={busy}
            onClick={onClose}
          >
            ×
          </button>
        </div>
        <h2 id="project-dialog-title">Add a project</h2>
        <div
          className="project-mode"
          role="group"
          aria-label="Project starting point"
        >
          <button
            type="button"
            aria-pressed={mode === "existing"}
            onClick={() => setMode("existing")}
          >
            Existing folders
          </button>
          <button
            type="button"
            aria-pressed={mode === "new"}
            onClick={() => setMode("new")}
          >
            New project
          </button>
        </div>
        <p className="dialog-description">
          {mode === "existing"
            ? "Connect work already on the daemon’s computer. Your assistant can keep track while you decide what comes next."
            : "Describe the outcome. Your assistant can help organize a new piece of work."}
        </p>
        <form onSubmit={create}>
          {mode === "existing" && (
            <section className="project-folder-choice">
              <strong>Project folders</strong>
              <DirectoryList
                paths={directories}
                onRemove={(path) =>
                  setDirectories((paths) =>
                    paths.filter((value) => value !== path),
                  )
                }
              />
              <button
                type="button"
                className="button secondary"
                onClick={() => setPicking(true)}
              >
                Choose folders
              </button>
              <p className="field-hint">
                These are references to existing folders. Adding them does not
                start any work or modify their contents.
              </p>
            </section>
          )}
          <label htmlFor="project-title">
            Project name
            <input
              id="project-title"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="A clear, useful name"
              maxLength={200}
              required
            />
          </label>
          {mode === "existing" ? (
            <details className="project-brief">
              <summary>Add a brief (optional)</summary>
              <p className="field-hint">
                Your assistant can help establish the outcome and acceptance
                criteria later. Tracking a folder does not need them.
              </p>
            <label htmlFor="project-description">
              Desired outcome
              <textarea
                id="project-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="What should change, and why does it matter?"
                rows={2}
                maxLength={20000}
                required={false}
              />
            </label>
            <label htmlFor="project-criteria">
              What does done look like?
              <textarea
                id="project-criteria"
                value={criteria}
                onChange={(e) => setCriteria(e.target.value)}
                placeholder="One acceptance criterion per line"
                rows={2}
                maxLength={20000}
                required={false}
              />
            </label>
            </details>
          ) : (
            <>
            <label htmlFor="project-description">
              Desired outcome
              <textarea
                id="project-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="What should change, and why does it matter?"
                rows={2}
                maxLength={20000}
                required={true}
              />
            </label>
            <label htmlFor="project-criteria">
              What does done look like?
              <textarea
                id="project-criteria"
                value={criteria}
                onChange={(e) => setCriteria(e.target.value)}
                placeholder="One acceptance criterion per line"
                rows={2}
                maxLength={20000}
                required={true}
              />
            </label>
            </>
          )}
          {error && (
            <div className="error-notice" role="alert">
              {error}
            </div>
          )}
          <div className="dialog-footer">
            <p>
              {mode === "existing"
                ? "You can track existing work now. Before delegation, your assistant will help establish the outcome and acceptance criteria."
                : "Creating a project records the outcome. Ask your assistant to begin coordination."}
            </p>
            <button
              className="button primary"
              disabled={
                busy ||
                !title.trim() ||
                (mode === "existing"
                  ? !directories.length
                  : !description.trim() || !criteria.trim())
              }
            >
              {busy
                ? "Adding…"
                : mode === "existing"
                  ? "Add project"
                  : "Create project"}
            </button>
          </div>
        </form>
      </dialog>
      {picking && (
        <FileSystemPicker
          kind="directory"
          multiple
          initialPath={directories[0]}
          onCancel={() => setPicking(false)}
          onSelect={(paths) => {
            setDirectories((previous) => [...new Set([...previous, ...paths])]);
            if (!title.trim() && paths.length)
              setTitle(directoryName(paths[0]));
            setPicking(false);
          }}
        />
      )}
    </>
  );
}

export function ProjectDirectories({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const [picking, setPicking] = useState(false);
  const [paths, setPaths] = useState(project.directories || []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save() {
    setBusy(true);
    setError("");
    try {
      await api(`/api/projects/${encodeURIComponent(project.id)}/directories`, {
        method: "PUT",
        body: JSON.stringify({ directories: paths }),
      });
      await refresh();
      setEditing(false);
    } catch (error) {
      setError(errorText(error));
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="project-locations">
      <div className="section-heading">
        <h3>Project folders</h3>
        {!editing && (
          <button
            type="button"
            className="text-button"
            onClick={() => {
              setPaths(project.directories || []);
              setError("");
              setEditing(true);
            }}
          >
            {project.directories?.length ? "Edit folders" : "Attach folders"}
          </button>
        )}
      </div>
      <DirectoryList
        paths={editing ? paths : project.directories || []}
        onRemove={
          editing && !busy
            ? (path) =>
                setPaths((previous) =>
                  previous.filter((value) => value !== path),
                )
            : undefined
        }
      />
      {!editing && !project.directories?.length && (
        <p className="field-hint">No existing folders attached.</p>
      )}
      {editing && (
        <div className="project-location-actions">
          <button
            type="button"
            className="button secondary"
            disabled={busy}
            onClick={() => setPicking(true)}
          >
            Choose folders
          </button>
          <button
            type="button"
            className="button primary"
            disabled={busy}
            onClick={() => void save()}
          >
            {busy ? "Saving…" : "Save folders"}
          </button>
          <button
            type="button"
            className="text-button"
            disabled={busy}
            onClick={() => setEditing(false)}
          >
            Cancel
          </button>
        </div>
      )}
      {error && (
        <div className="error-notice" role="alert">
          {error}
        </div>
      )}
      {project.scratch_directory && (
        <details className="project-scratch">
          <summary>Assistant scratch folder</summary>
          <p className="field-hint">
            A separate location for this project’s coordination notes and
            artifacts.
          </p>
          <code>{project.scratch_directory}</code>
        </details>
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

import { useAction } from "./ui";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, createProject, criteriaLines, type Project } from "./api";
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
type ProjectKind = "draft" | "track";

export function NewProject({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => Promise<void>;
}) {
  const [kind, setKind] = useState<ProjectKind>("draft");
  const [title, setTitle] = useState("");
  const [goal, setGoal] = useState("");
  const [audience, setAudience] = useState("");
  const [constraints, setConstraints] = useState("");
  const [criteria, setCriteria] = useState("");
  const [directories, setDirectories] = useState<string[]>([]);
  const [picking, setPicking] = useState(false);
  const { busy, error, run } = useAction();
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => element?.close();
  }, []);
  async function create(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await createProject({
        title: title.trim(),
        directories,
        brief: {
          goal: goal.trim(),
          audience: audience.trim(),
          constraints: constraints.trim(),
          criteria: criteriaLines(criteria),
        },
        template: kind === "draft" ? "draft" : "",
      });
      await onCreated();
    });
  }
  const goalRequired = kind === "draft";
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
          aria-label="How the work gets done"
        >
          <button
            type="button"
            aria-pressed={kind === "draft"}
            onClick={() => setKind("draft")}
          >
            Written work (writer + reviewer)
          </button>
          <button
            type="button"
            aria-pressed={kind === "track"}
            onClick={() => setKind("track")}
          >
            Just track it
          </button>
        </div>
        <p className="dialog-description">
          {kind === "draft"
            ? "A writer drafts what you ask for and a reviewer checks it against the brief. Nothing leaves the project until you approve it."
            : "Keep the project and its folders in view. You can choose a team later."}
        </p>
        <form onSubmit={create}>
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
          <label htmlFor="project-goal">
            {goalRequired ? "Goal" : "Goal (optional)"}
            <textarea
              id="project-goal"
              value={goal}
              onChange={(e) => setGoal(e.target.value)}
              placeholder="What is this project for?"
              rows={2}
              maxLength={20000}
              required={goalRequired}
            />
          </label>
          <details className="project-brief">
            <summary>More about the brief (optional)</summary>
            <BriefFields
              idPrefix="project"
              audience={audience}
              constraints={constraints}
              criteria={criteria}
              onAudience={setAudience}
              onConstraints={setConstraints}
              onCriteria={setCriteria}
            />
          </details>
          <section className="project-folder-choice">
            <strong>Project folders (optional)</strong>
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
              References to existing folders. Adding them does not start any
              work or modify their contents.
            </p>
          </section>
          {error && (
            <div className="error-notice" role="alert">
              {error}
            </div>
          )}
          <div className="dialog-footer">
            <p>
              Creating a project starts no work. Ask for something on its page
              when you are ready.
            </p>
            <button
              className="button primary"
              disabled={busy || !title.trim() || (goalRequired && !goal.trim())}
            >
              {busy ? "Adding…" : "Create project"}
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

/** The optional parts of a brief, shared by the new-project form and the brief editor. */
export function BriefFields({
  idPrefix,
  audience,
  constraints,
  criteria,
  onAudience,
  onConstraints,
  onCriteria,
}: {
  idPrefix: string;
  audience: string;
  constraints: string;
  criteria: string;
  onAudience: (value: string) => void;
  onConstraints: (value: string) => void;
  onCriteria: (value: string) => void;
}) {
  return (
    <>
      <label htmlFor={`${idPrefix}-audience`}>
        Audience
        <textarea
          id={`${idPrefix}-audience`}
          value={audience}
          onChange={(e) => onAudience(e.target.value)}
          placeholder="Who is it for?"
          rows={2}
          maxLength={20000}
        />
      </label>
      <label htmlFor={`${idPrefix}-constraints`}>
        Constraints
        <textarea
          id={`${idPrefix}-constraints`}
          value={constraints}
          onChange={(e) => onConstraints(e.target.value)}
          placeholder="Length, tone, anything to avoid"
          rows={2}
          maxLength={20000}
        />
      </label>
      <label htmlFor={`${idPrefix}-criteria`}>
        What does done look like?
        <textarea
          id={`${idPrefix}-criteria`}
          value={criteria}
          onChange={(e) => onCriteria(e.target.value)}
          placeholder="One criterion per line"
          rows={3}
          maxLength={20000}
        />
      </label>
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
  const { busy, error, setError, run } = useAction();
  async function save() {
    await run(async () => {
      await api(`/api/projects/${encodeURIComponent(project.id)}/directories`, {
        method: "PUT",
        body: JSON.stringify({ directories: paths }),
      });
      await refresh();
      setEditing(false);
    });
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

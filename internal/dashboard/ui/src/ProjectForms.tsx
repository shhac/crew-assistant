import { useEffect, useRef, useState, type FormEvent } from "react";
import { FileSystemPicker } from "./FileSystemPicker";
import { ErrorNotice, Icon, useAction } from "./ui";
import { createProject, criteriaLines, setTeam } from "./api";

function directoryName(path: string) {
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

type Kind = "writing" | "code" | "tracking";

const kinds: { kind: Kind; label: string; hint: string }[] = [
  {
    kind: "writing",
    label: "Writing",
    hint: "A writer drafts what you ask for, and a reviewer checks it against the brief.",
  },
  {
    kind: "code",
    label: "Code",
    hint: "An implementer changes a private copy of the repository, a reviewer reads the change, and QA runs your check. Changes land as a new local branch until you choose otherwise.",
  },
  {
    kind: "tracking",
    label: "Tracking only",
    hint: "Keep it in view. You can add a team later.",
  },
];

export function NewProject({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (id: string) => Promise<void>;
}) {
  const [kind, setKind] = useState<Kind>("writing");
  const [title, setTitle] = useState("");
  const [goal, setGoal] = useState("");
  const [audience, setAudience] = useState("");
  const [constraints, setConstraints] = useState("");
  const [criteria, setCriteria] = useState("");
  const [check, setCheck] = useState("");
  const [directories, setDirectories] = useState<string[]>([]);
  const [picking, setPicking] = useState(false);
  const [madeWithoutTeam, setMadeWithoutTeam] = useState("");
  const { busy, error, setError, run } = useAction();
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => element?.close();
  }, []);
  const code = kind === "code";
  const goalRequired = kind !== "tracking";
  async function create(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      const project = await createProject({
        title: title.trim(),
        directories,
        brief: {
          goal: goal.trim(),
          audience: audience.trim(),
          constraints: constraints.trim(),
          criteria: criteriaLines(criteria),
        },
        template: kind === "writing" ? "draft" : "",
      });
      if (code) {
        try {
          await setTeam(project.id, {
            template: "code",
            writer_engine: "",
            reviewer_engine: "",
            max_rounds: "",
            deliver_to: "",
            repo: directories[0],
            branch_prefix: "",
            check: check.trim(),
            prepare: [],
            sign: "",
          });
        } catch (failure) {
          setMadeWithoutTeam(project.id);
          throw failure;
        }
      }
      await onCreated(project.id);
    });
  }
  if (madeWithoutTeam)
    return (
      <dialog
        ref={dialog}
        className="dialog"
        aria-labelledby="new-project-title"
        onCancel={onClose}
      >
        <h2 id="new-project-title">The project was made without its team</h2>
        <ErrorNotice error={error} />
        <p className="soft">Set the team up on the project's Team tab.</p>
        <div className="actions">
          <button
            className="btn btn-primary"
            onClick={() => void onCreated(madeWithoutTeam)}
          >
            Open the project
          </button>
        </div>
      </dialog>
    );
  const ready =
    title.trim() &&
    (!goalRequired || goal.trim()) &&
    (!code || (directories.length > 0 && check.trim()));
  return (
    <>
      <dialog
        ref={dialog}
        className="dialog"
        aria-labelledby="new-project-title"
        onCancel={(e) => {
          if (busy) e.preventDefault();
          else onClose();
        }}
      >
        <div className="dialog-head">
          <h2 id="new-project-title">New project</h2>
          <button
            type="button"
            className="btn btn-quiet btn-icon"
            aria-label="Close"
            disabled={busy}
            onClick={onClose}
          >
            <Icon name="Close" />
          </button>
        </div>
        <form className="form" onSubmit={create}>
          <div className="segmented" role="group" aria-label="Kind of project">
            {kinds.map((k) => (
              <button
                key={k.kind}
                type="button"
                aria-pressed={kind === k.kind}
                onClick={() => {
                  setKind(k.kind);
                  setError("");
                }}
              >
                {k.label}
              </button>
            ))}
          </div>
          <p className="hint">{kinds.find((k) => k.kind === kind)?.hint}</p>
          <label htmlFor="project-title">
            Name
            <input
              id="project-title"
              className="field"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              maxLength={200}
              required
            />
          </label>
          <label htmlFor="project-goal">
            Goal{goalRequired ? "" : " (optional)"}
            <textarea
              id="project-goal"
              className="field"
              value={goal}
              onChange={(e) => setGoal(e.target.value)}
              placeholder="What is this project for?"
              rows={2}
              maxLength={20000}
              required={goalRequired}
            />
          </label>
          <details className="disclosure">
            <summary>More about the brief</summary>
            <div className="form disclosure-body">
              <BriefFields
                idPrefix="project"
                audience={audience}
                constraints={constraints}
                criteria={criteria}
                onAudience={setAudience}
                onConstraints={setConstraints}
                onCriteria={setCriteria}
              />
            </div>
          </details>
          <div className="control">
            {code ? "Repository" : "Folders (optional)"}
            {directories.length > 0 && (
              <DirectoryList
                paths={directories}
                onRemove={(path) =>
                  setDirectories((paths) =>
                    paths.filter((value) => value !== path),
                  )
                }
              />
            )}
            <div className="actions">
              <button
                type="button"
                className="btn btn-sm"
                onClick={() => setPicking(true)}
              >
                {code
                  ? directories.length
                    ? "Change repository"
                    : "Choose repository"
                  : "Add folders"}
              </button>
            </div>
          </div>
          {code && (
            <label htmlFor="project-check">
              QA runs
              <input
                id="project-check"
                className="field"
                value={check}
                placeholder="make check"
                onChange={(e) => setCheck(e.target.value)}
                required
              />
            </label>
          )}
          <ErrorNotice error={error} />
          <div className="actions dialog-foot">
            <button className="btn btn-primary" disabled={busy || !ready}>
              Create project
            </button>
            <button
              type="button"
              className="btn btn-quiet"
              disabled={busy}
              onClick={onClose}
            >
              Cancel
            </button>
          </div>
        </form>
      </dialog>
      {picking && (
        <FileSystemPicker
          kind="directory"
          multiple={!code}
          initialPath={directories[0]}
          onCancel={() => setPicking(false)}
          onSelect={(paths) => {
            setDirectories((previous) =>
              code ? paths.slice(0, 1) : [...new Set([...previous, ...paths])],
            );
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
        Who it's for
        <input
          id={`${idPrefix}-audience`}
          className="field"
          value={audience}
          onChange={(e) => onAudience(e.target.value)}
          maxLength={20000}
        />
      </label>
      <label htmlFor={`${idPrefix}-constraints`}>
        Constraints
        <textarea
          id={`${idPrefix}-constraints`}
          className="field"
          value={constraints}
          onChange={(e) => onConstraints(e.target.value)}
          placeholder="Length, tone, anything to avoid"
          rows={2}
          maxLength={20000}
        />
      </label>
      <label htmlFor={`${idPrefix}-criteria`}>
        Done when
        <textarea
          id={`${idPrefix}-criteria`}
          className="field"
          value={criteria}
          onChange={(e) => onCriteria(e.target.value)}
          placeholder="One point per line"
          rows={3}
          maxLength={20000}
        />
      </label>
    </>
  );
}

import { Icon } from "./ui";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { errorText, type FileSystemPage, type FileSystemKind } from "./api";
import { DirectoryTree, listDirectory } from "./DirectoryTree";

export interface FileSystemPickerProps {
  kind: FileSystemKind;
  multiple?: boolean;
  initialPath?: string;
  onSelect: (paths: string[]) => void;
  onCancel: () => void;
}

/** Browses the daemon's filesystem; no browser upload or local filesystem API. */
export function FileSystemPicker({
  kind,
  multiple = false,
  initialPath = "",
  onSelect,
  onCancel,
}: FileSystemPickerProps) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [target, setTarget] = useState(initialPath);
  const [draft, setDraft] = useState(initialPath);
  const [hidden, setHidden] = useState(false);
  const [revision, setRevision] = useState(0);
  const [page, setPage] = useState<FileSystemPage | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => element?.close();
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError("");
    setPage(null);
    listDirectory(target, kind, hidden, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) {
          setPage(value);
          setDraft(value.path);
        }
      })
      .catch((error) => {
        if (!controller.signal.aborted) setError(errorText(error));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [target, kind, hidden, revision]);
  function jump(e: FormEvent) {
    e.preventDefault();
    setTarget(draft.trim());
    setRevision((value) => value + 1);
  }
  function chooseCurrent() {
    if (!page) return;
    setSelected((previous) =>
      multiple ? [...new Set([...previous, page.path])] : [page.path],
    );
  }
  return (
    <dialog
      ref={dialog}
      className="dialog picker"
      aria-labelledby="filesystem-title"
      onCancel={(e) => {
        e.preventDefault();
        onCancel();
      }}
    >
      <div className="filesystem-header">
        <div>
          <h2 id="filesystem-title">
            Choose{" "}
            {kind === "directory"
              ? "folders"
              : kind === "file"
                ? "files"
                : "files or folders"}
          </h2>
        </div>
        <button
          type="button"
          className="btn btn-quiet btn-icon"
          aria-label="Close"
          onClick={onCancel}
        >
          <Icon name="Close" />
        </button>
      </div>
      <div className="filesystem-body">
        <p className="muted small">On the computer running crew-assistant.</p>
        <form className="filesystem-location" onSubmit={jump}>
          <label htmlFor="filesystem-path">
            Path
            <input
              id="filesystem-path"
              className="field"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              placeholder="Home folder"
              spellCheck={false}
            />
          </label>
          <button className="btn" disabled={loading}>
            Go
          </button>
        </form>
        <div className="filesystem-toolbar">
          <button
            type="button"
            className="btn btn-quiet btn-sm"
            disabled={loading || !page?.parent}
            onClick={() => page?.parent && setTarget(page.parent)}
          >
            <Icon name="Up" size={14} /> Up a folder
          </button>
          <label className="filesystem-hidden">
            <input
              type="checkbox"
              checked={hidden}
              onChange={(e) => setHidden(e.target.checked)}
            />
            Show hidden
          </label>
        </div>
        {error && (
          <div className="error" role="alert">
            {error}{" "}
            <button
              type="button"
              className="link-button"
              onClick={() => setRevision((value) => value + 1)}
            >
              Try again
            </button>
          </div>
        )}
        {loading && (
          <div className="filesystem-loading" role="status">
            Loading…
          </div>
        )}
        {page && !loading && (
          <DirectoryTree
            key={`${page.path}:${hidden}:${revision}`}
            page={page}
            kind={kind}
            hidden={hidden}
            multiple={multiple}
            selected={selected}
            onChange={setSelected}
          />
        )}
        {page && kind !== "file" && (
          <div className="filesystem-current-row">
            <button
              type="button"
              className="btn filesystem-current"
              onClick={chooseCurrent}
            >
              Choose this folder: <code>{page.path}</code>
            </button>
          </div>
        )}
        <div className="filesystem-selection" aria-live="polite">
          <strong>
            {selected.length
              ? `${selected.length} selected`
              : "Nothing selected"}
          </strong>
          {selected.length > 0 && (
            <ul>
              {selected.map((path) => (
                <li key={path}>
                  <span title={path}>{path}</span>
                  <button
                    type="button"
                    className="btn btn-quiet btn-icon btn-sm"
                    aria-label={`Remove selection ${path}`}
                    onClick={() =>
                      setSelected((previous) =>
                        previous.filter((value) => value !== path),
                      )
                    }
                  >
                    ×
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
      <div className="picker-foot">
        <p className="muted small">
          {multiple
            ? "Click to choose; Shift-click for a range. Arrow keys move, Enter opens a folder."
            : "Click to choose. Arrow keys move, Enter opens a folder."}
        </p>
        <button type="button" className="btn btn-quiet" onClick={onCancel}>
          Cancel
        </button>
        <button
          type="button"
          className="btn btn-primary"
          disabled={!selected.length}
          onClick={() => onSelect(selected)}
        >
          {selected.length > 1 ? "Use these" : "Use this"}
        </button>
      </div>
    </dialog>
  );
}

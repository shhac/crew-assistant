import { useState, type FormEvent } from "react";
import {
  BrowserFields,
  REAL_CHROME,
  savedBrowser,
  useBrowserOffered,
} from "./BrowserFields";
import { seatFor } from "./members";
import { ErrorNotice, useAction } from "./ui";
import { setQABrowser, type Browser, type Playbook, type Project } from "./api";

/** Whether this project's QA uses the browser, which the owner can change. */
export function QABrowser({
  project,
  playbook,
  refresh,
}: {
  project: Project;
  playbook: Playbook;
  refresh: () => Promise<void>;
}) {
  const qa = seatFor(playbook, "qa");
  const offered = useBrowserOffered(qa?.engine ?? "");
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState<Browser>(qa?.browser ?? {});
  const { busy, error, run } = useAction();
  if (!qa || (!offered && !qa.browser?.on)) return null;
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await setQABrowser(project.id, savedBrowser(value));
      await refresh();
      setEditing(false);
    });
  }
  if (editing)
    return (
      <form className="form section" aria-label="QA's browser" onSubmit={save}>
        <BrowserFields
          id="seat"
          engine={qa.engine}
          value={value}
          onChange={setValue}
        />
        <ErrorNotice error={error} />
        <div className="actions">
          <button className="btn btn-primary" type="submit" disabled={busy}>
            Save
          </button>
          <button
            className="btn btn-quiet"
            type="button"
            disabled={busy}
            onClick={() => setEditing(false)}
          >
            Cancel
          </button>
        </div>
      </form>
    );
  const on = qa.browser?.on;
  return (
    <div className="section" aria-label="QA's browser">
      <div className="panel-head">
        <p className="label">QA's browser</p>
        <button
          type="button"
          className="btn btn-sm"
          onClick={() => {
            setValue(qa.browser ?? {});
            setEditing(true);
          }}
        >
          Edit
        </button>
      </div>
      <p className={on ? "" : "muted"}>
        {on
          ? `On, ${qa.browser?.name ? `using the connected browser “${qa.browser.name}”` : "using the browser the extension connects by default"}. ${REAL_CHROME}`
          : "Off. QA doesn't use a browser."}
      </p>
      <p className="hint">
        Used only when the project has a run recipe, to try the app.
      </p>
    </div>
  );
}

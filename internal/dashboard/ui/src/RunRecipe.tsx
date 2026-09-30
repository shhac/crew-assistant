import { useState, type FormEvent } from "react";
import { ErrorNotice, useAction } from "./ui";
import {
  setRunRecipe,
  type Playbook,
  type Project,
  type RunRecipe,
} from "./api";

const blank: RunRecipe = { setup: "", start: "", url: "", ready: "" };

/**
 * How QA starts a code project's app and uses it, as well as running the
 * check. Without a recipe, QA runs the check only.
 */
export function RunRecipeSettings({
  project,
  playbook,
  refresh,
}: {
  project: Project;
  playbook: Playbook;
  refresh: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const recipe = playbook.run;
  if (editing)
    return (
      <section className="tab-panel card" aria-label="Running the app">
        <RunRecipeEditor
          project={project}
          recipe={recipe}
          onDone={() => setEditing(false)}
          refresh={refresh}
        />
      </section>
    );
  return (
    <section className="tab-panel card" aria-label="Running the app">
      <div className="panel-head">
        <h3>Running the app</h3>
        <button
          type="button"
          className="btn btn-sm"
          onClick={() => setEditing(true)}
        >
          {recipe ? "Edit" : "Add a recipe"}
        </button>
      </div>
      {recipe ? (
        <dl className="facts">
          {recipe.setup && (
            <div className="fact-row">
              <dt>Setup</dt>
              <dd>
                <code>{recipe.setup}</code>
              </dd>
            </div>
          )}
          <div className="fact-row">
            <dt>Start</dt>
            <dd>
              <code>{recipe.start}</code>
            </dd>
          </div>
          <div className="fact-row">
            <dt>Answers at</dt>
            <dd>
              <code>{recipe.url}</code>
            </dd>
          </div>
          <div className="fact-row">
            <dt>Ready when</dt>
            <dd>
              {recipe.ready ? (
                <>
                  <code>{recipe.ready}</code> succeeds
                </>
              ) : (
                "The address answers"
              )}
            </dd>
          </div>
        </dl>
      ) : (
        <p className="muted">
          No recipe, so QA runs the check only. With one, QA also starts the app
          on this machine and tries it.
        </p>
      )}
    </section>
  );
}

function RunRecipeEditor({
  project,
  recipe,
  onDone,
  refresh,
}: {
  project: Project;
  recipe?: RunRecipe;
  onDone: () => void;
  refresh: () => Promise<void>;
}) {
  const [value, setValue] = useState<RunRecipe>({ ...blank, ...recipe });
  const { busy, error, run } = useAction();
  const field = (key: keyof RunRecipe) => (e: { target: { value: string } }) =>
    setValue((v) => ({ ...v, [key]: e.target.value }));
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await setRunRecipe(project.id, {
        setup: value.setup?.trim() ?? "",
        start: value.start.trim(),
        url: value.url.trim(),
        ready: value.ready?.trim() ?? "",
      });
      await refresh();
      onDone();
    });
  }
  async function remove() {
    await run(async () => {
      await setRunRecipe(project.id, null);
      await refresh();
      onDone();
    });
  }
  return (
    <form className="form" aria-label="Running the app" onSubmit={save}>
      <h3>Running the app</h3>
      <p className="hint">
        QA starts the app from a copy of each change, on this machine only:
        nothing else on the network can be reached, so setup runs offline.
        Dependencies come from the ignored folders copied in under Workspace,
        such as <code>node_modules</code>.
      </p>
      <label htmlFor="run-setup">
        Setup
        <input
          id="run-setup"
          className="field"
          value={value.setup ?? ""}
          maxLength={500}
          placeholder="npm run build"
          onChange={field("setup")}
        />
        <span className="hint">
          Optional. Runs once, before the app starts.
        </span>
      </label>
      <label htmlFor="run-start">
        Start
        <input
          id="run-start"
          className="field"
          value={value.start}
          maxLength={500}
          placeholder="npm start"
          onChange={field("start")}
          required
        />
        <span className="hint">
          Runs in the background. Each check has a port of its own, in{" "}
          <code>PORT</code>.
        </span>
      </label>
      <label htmlFor="run-url">
        Address
        <input
          id="run-url"
          className="field"
          value={value.url}
          maxLength={300}
          placeholder="http://127.0.0.1:{port}/"
          onChange={field("url")}
          required
        />
        <span className="hint">
          On 127.0.0.1, localhost or [::1], with <code>{"{port}"}</code> as
          its port, so each check reaches its own app.
        </span>
      </label>
      <label htmlFor="run-ready">
        Ready when
        <input
          id="run-ready"
          className="field"
          value={value.ready ?? ""}
          maxLength={500}
          placeholder="curl -sf http://127.0.0.1:{port}/health"
          onChange={field("ready")}
        />
        <span className="hint">
          Optional. A command that succeeds once the app is ready; empty waits
          until the address answers.
        </span>
      </label>
      <p className="hint">
        Requests already under way keep the recipe they started with.
      </p>
      <ErrorNotice error={error} />
      <div className="actions">
        <button className="btn btn-primary" type="submit" disabled={busy}>
          Save
        </button>
        {recipe && (
          <button
            className="btn btn-quiet"
            type="button"
            disabled={busy}
            onClick={() => void remove()}
          >
            Remove recipe
          </button>
        )}
        <button
          className="btn btn-quiet"
          type="button"
          disabled={busy}
          onClick={onDone}
        >
          Cancel
        </button>
      </div>
    </form>
  );
}

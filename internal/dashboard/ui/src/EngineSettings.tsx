import { useEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { ProviderIcon } from "./ProviderIcon";
import { useModelCatalog } from "./modelCatalog";
import { ConfirmAction, ErrorNotice, Icon, Pill, useAction } from "./ui";
import {
  legacyProvider,
  section,
  withEngine,
  type APIProviderSettings,
  type Config,
  type ConfigDefaults,
  type EffortParameter,
  type EngineChoice,
} from "./api";
import { choicesFor, useEngineChoices } from "./engines";

type SaveEngines = (engines: Config["engines"]) => Promise<void>;
export function hostSummary(address: string) {
  try {
    return new URL(address).host || "No API address set";
  } catch {
    return "No API address set";
  }
}

export function EngineSettings({
  config,
  defaults,
  onSave,
  disabled = false,
}: {
  config: Config;
  defaults?: ConfigDefaults;
  onSave: SaveEngines;
  disabled?: boolean;
}) {
  const choices = useEngineChoices();
  const [editing, setEditing] = useState<number | null>(null);
  const invoker = useRef<HTMLButtonElement | null>(null);
  const add = useRef<HTMLButtonElement>(null);
  const providers = providersOf(config);
  const open = (index: number, button: HTMLButtonElement) => {
    invoker.current = button;
    setEditing(index);
  };
  const close = (removed = false) => {
    setEditing(null);
    (removed ? add.current : invoker.current)?.focus();
  };
  // Design departure: summarize the default endpoint when the address is blank,
  // rather than saying no address is set, because the daemon uses that endpoint.
  const address = String(
    section(section(config.engines)[legacyProvider]).base_url ||
      defaults?.openai_base_url ||
      "",
  );
  return (
    <div className="form engine-settings">
      <div className="engine-grid">
        {choicesFor(choices, "cli").map((choice) => (
          <EngineCard
            key={choice.engine}
            {...{ choice, config, defaults, onSave, disabled }}
          />
        ))}
      </div>
      <div className="api-connections-head">
        <h3>API connections</h3>
        <button
          ref={add}
          disabled={disabled}
          className="btn btn-sm"
          type="button"
          onClick={(e) => open(-2, e.currentTarget)}
        >
          Add API connection
        </button>
      </div>
      <ul className="rows">
        {[
          { name: "OpenAI-compatible (default)", base_url: address },
          ...providers,
        ].map((p, i) => (
          <li key={i}>
            <button
              type="button"
              className="connection-row"
              disabled={disabled}
              onClick={(e) => open(i - 1, e.currentTarget)}
            >
              <ProviderIcon engine={legacyProvider} size={24} />
              <span>
                <strong>{p.name}</strong>
                <span className="muted small connection-summary">
                  {hostSummary(p.base_url)}
                </span>
              </span>
              <Icon name="Chevron" />
            </button>
          </li>
        ))}
      </ul>
      {!providers.length && (
        <p className="hint">No additional API connections.</p>
      )}
      {editing !== null && (
        <EngineDialog
          key={editing}
          {...{ config, defaults, onSave }}
          index={editing}
          onClose={close}
        />
      )}
    </div>
  );
}

function EngineCard({
  choice,
  config,
  defaults,
  onSave,
  disabled,
}: {
  choice: EngineChoice;
  config: Config;
  defaults?: ConfigDefaults;
  onSave: SaveEngines;
  disabled?: boolean;
}) {
  const catalog = useModelCatalog(choice.engine);
  const [open, setOpen] = useState(false);
  const button = useRef<HTMLButtonElement>(null);
  const checking = catalog.loading || (!catalog.catalog && !catalog.error);
  const connected = !checking && catalog.catalog?.available;
  return (
    <>
      <button
        ref={button}
        disabled={disabled}
        type="button"
        className="card engine-card"
        onClick={() => setOpen(true)}
      >
        <ProviderIcon engine={choice.engine} size={24} />
        <span className="engine-card-text">
          <strong>{choice.label}</strong>
          <span className="muted small">Command and sign-in folder</span>
        </span>
        <Pill dot tone={checking ? "work" : connected ? "done" : "wait"}>
          {checking ? "Checking…" : connected ? "Connected" : "Not connected"}
        </Pill>
        <Icon name="Chevron" />
      </button>
      {open && (
        <EngineDialog
          {...{ choice, config, defaults }}
          onSave={async (engines) => {
            await onSave(engines);
            catalog.refresh();
          }}
          onClose={() => {
            setOpen(false);
            button.current?.focus();
          }}
        />
      )}
    </>
  );
}

function EngineDialog({
  choice,
  config,
  defaults,
  index = -1,
  onSave,
  onClose,
}: {
  choice?: EngineChoice;
  config: Config;
  defaults?: ConfigDefaults;
  index?: number;
  onSave: SaveEngines;
  onClose: (removed?: boolean) => void;
}) {
  const named = !choice && index !== -1;
  const providers = providersOf(config);
  const original = named && index >= 0 ? providers[index] : null;
  const [local, setLocal] = useState<Config>(() =>
    named
      ? {
          ...config,
          engines: {
            ...section(config.engines),
            providers: [
              original ?? { id: "", name: "", base_url: "", api_key_env: "" },
            ],
          },
        }
      : config,
  );
  const [confirming, setConfirming] = useState(false);
  const action = useAction();
  const running = useRef(false);
  const closed = useRef(false);
  const backdropStart = useRef(false);
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current!;
    closed.current = false;
    dialog.showModal();
    return () => {
      closed.current = true;
      dialog.close();
    };
  }, []);
  const close = (removed = false) => {
    if (closed.current) return;
    closed.current = true;
    // Release modal inertness before the parent restores outside focus.
    ref.current?.close();
    onClose(removed);
  };
  const dismiss = () => {
    if (!running.current) close();
  };
  const onBackdrop = (event: {
    target: EventTarget;
    currentTarget: HTMLDialogElement;
    clientX: number;
    clientY: number;
  }) => {
    const rect = event.currentTarget.getBoundingClientRect();
    return (
      event.target === event.currentTarget &&
      (event.clientX < rect.left ||
        event.clientX > rect.right ||
        event.clientY < rect.top ||
        event.clientY > rect.bottom)
    );
  };
  const save = async (remove = false) => {
    if (running.current) return;
    running.current = true;
    await action.run(async () => {
      let engines = local.engines;
      if (named) {
        const next = { ...providersOf(local)[0] };
        if (!next.effort_parameter) delete next.effort_parameter;
        const list = remove
          ? providers.filter((_, i) => i !== index)
          : index >= 0
            ? providers.map((p, i) => (i === index ? next : p))
            : [...providers, next];
        engines = { ...section(config.engines) };
        if (list.length) engines.providers = list;
        else delete engines.providers;
      }
      await onSave(engines);
      close(remove);
    });
    running.current = false;
  };
  const title = choice
    ? `Edit ${choice.label}`
    : named
      ? original
        ? `Edit ${original.name}`
        : "Add API connection"
      : "Edit OpenAI-compatible API";
  return createPortal(
    <dialog
      ref={ref}
      className="dialog engine-dialog"
      aria-label={title}
      onClose={(e) => {
        // StrictMode may reopen after effect cleanup before its queued close event.
        if (!e.currentTarget.open) {
          // A non-cancelable browser close must not hide a pending save or its
          // error, or allow another editor to save a stale engines snapshot.
          if (running.current && !closed.current) e.currentTarget.showModal();
          else close();
        }
      }}
      onCancel={(e) => {
        e.preventDefault();
        dismiss();
      }}
      onMouseDown={(e) => {
        backdropStart.current = onBackdrop(e);
      }}
      onClick={(e) => {
        const dismissBackdrop = backdropStart.current && onBackdrop(e);
        backdropStart.current = false;
        if (dismissBackdrop) dismiss();
      }}
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          e.stopPropagation();
          void save();
        }}
      >
        <div className="dialog-head">
          <ProviderIcon engine={choice?.engine ?? legacyProvider} size={24} />
          <h2>{title}</h2>
          <button
            type="button"
            className="btn btn-quiet"
            aria-label="Close"
            disabled={action.busy}
            onClick={dismiss}
          >
            <Icon name="Close" />
          </button>
        </div>
        <div className="engine-dialog-body">
          <fieldset className="engine-dialog-fields" disabled={action.busy}>
            <EngineFields
              config={local}
              defaults={defaults}
              choice={choice}
              named={named}
              onChange={setLocal}
            />
          </fieldset>
          <ErrorNotice error={action.error} />
        </div>
        <div className="actions dialog-foot">
          {original && (
            <div className="engine-remove">
              <ConfirmAction
                confirming={confirming}
                onConfirming={setConfirming}
                trigger="Remove connection"
                triggerClassName="btn-danger"
                confirm="Remove connection"
                busy={action.busy}
                onConfirm={() => {
                  void save(true);
                }}
              />
            </div>
          )}
          <button
            type="button"
            className="btn btn-quiet"
            disabled={action.busy}
            onClick={dismiss}
          >
            Cancel
          </button>
          <button className="btn btn-primary" disabled={action.busy}>
            Save
          </button>
        </div>
      </form>
    </dialog>,
    document.body,
  );
}

/**
 * How the daemon reaches each engine, for every assistant, member and small
 * model alike: the CLIs and their logins, and the API's address and key.
 */
function EngineFields({
  config,
  defaults,
  onChange,
  choice,
  named,
}: {
  config: Config;
  defaults?: ConfigDefaults;
  onChange: (value: Config) => void;
  choice?: EngineChoice;
  named?: boolean;
}) {
  const setting = (engine: string, key: string) =>
    String(section(section(config.engines)[engine])[key] ?? "");
  const change = (engine: string, key: string, next: string) =>
    onChange(withEngine(config, engine, { [key]: next }));
  const effortParameter: EffortParameter =
    setting("openai-compatible", "effort_parameter") === "reasoning.effort"
      ? "reasoning.effort"
      : "";
  return (
    <div className="form">
      {!named && (
        <p className="hint">
          Optional; a blank field uses what it shows.
          {choice &&
            " Lists of models use the saved login and program, so save changes here before refreshing one."}
        </p>
      )}
      {choice && (
        <CLISettings
          choice={choice}
          defaults={defaults?.engines?.[choice.engine]}
          setting={(key) => setting(choice.engine, key)}
          change={(key, next) => change(choice.engine, key, next)}
        />
      )}
      {!choice && !named && (
        <>
          <label htmlFor="engines-openai-compatible-base_url">
            API address
            <input
              type="url"
              id="engines-openai-compatible-base_url"
              value={setting("openai-compatible", "base_url")}
              onChange={(e) =>
                change("openai-compatible", "base_url", e.target.value)
              }
              placeholder={defaults?.openai_base_url}
            />
          </label>
          <label htmlFor="engines-openai-compatible-api_key_env">
            API key variable
            <input
              id="engines-openai-compatible-api_key_env"
              value={setting("openai-compatible", "api_key_env")}
              onChange={(e) =>
                change("openai-compatible", "api_key_env", e.target.value)
              }
              pattern="[A-Za-z_][A-Za-z0-9_]*"
              autoComplete="off"
            />
            <span className="hint">
              The environment variable's name, never the key. Blank sends no
              key.
            </span>
          </label>
          <label htmlFor="engines-openai-compatible-effort_parameter">
            Reasoning effort is sent as
            <select
              id="engines-openai-compatible-effort_parameter"
              value={effortParameter}
              onChange={(e) =>
                change("openai-compatible", "effort_parameter", e.target.value)
              }
            >
              <option value="">reasoning_effort</option>
              <option value="reasoning.effort">reasoning.effort</option>
            </select>
            <span className="hint">
              OpenAI and xAI read reasoning_effort; gateways such as Vercel AI
              Gateway and OpenRouter read reasoning.effort.
            </span>
          </label>
        </>
      )}
      {named && <APIProviders config={config} onChange={onChange} />}
    </div>
  );
}

/** A provider's id from its name: lower-case letters, digits and hyphens. */
export function providerID(name: string) {
  const id = name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 64)
    .replace(/-+$/, "");
  return id === legacyProvider ? `${id}-2` : id;
}

function providersOf(config: Config): APIProviderSettings[] {
  const list = section(config.engines).providers;
  return Array.isArray(list) ? (list as APIProviderSettings[]) : [];
}

/**
 * Further named API providers beside the one above, such as OpenRouter next
 * to a model on this machine. Each is an address, a key variable and where
 * it reads reasoning effort; its id, which saved models name, follows its
 * name until changed.
 */
function APIProviders({
  config,
  onChange,
}: {
  config: Config;
  onChange: (value: Config) => void;
}) {
  const providers = providersOf(config);
  const [manualID, setManualID] = useState(false);
  const save = (next: APIProviderSettings[]) => {
    const engines = { ...section(config.engines) };
    if (next.length === 0) delete engines.providers;
    else engines.providers = next;
    onChange({ ...config, engines });
  };
  const change = (i: number, fields: Partial<APIProviderSettings>) =>
    save(
      providers.map((p, at) => {
        if (at !== i) return p;
        const next = { ...p, ...fields };
        // Drop a blank effort parameter so the default applies.
        if (!next.effort_parameter) delete next.effort_parameter;
        return next;
      }),
    );
  const rename = (i: number, name: string) => {
    const p = providers[i];
    const following = !manualID && (!p.id || p.id === providerID(p.name));
    change(i, following ? { name, id: providerID(name) } : { name });
  };
  return (
    <>
      {providers.map((p, i) => {
        const key = `engines-providers-${i}`;
        return (
          <fieldset key={i} className="engine-dialog-fields form">
            <legend className="sr-only">{p.name || "New API provider"}</legend>
            <label htmlFor={`${key}-name`}>
              Name
              <input
                id={`${key}-name`}
                value={p.name}
                required
                maxLength={80}
                onChange={(e) => rename(i, e.target.value)}
                autoComplete="off"
              />
            </label>
            <label htmlFor={`${key}-id`}>
              Id
              <input
                id={`${key}-id`}
                value={p.id}
                maxLength={64}
                pattern="[a-z0-9][a-z0-9-]*"
                onChange={(e) => {
                  setManualID(true);
                  change(i, { id: e.target.value });
                }}
                autoComplete="off"
              />
              <span className="hint">
                What saved models name it by; changing it leaves them on a
                provider that's gone.
              </span>
            </label>
            <label htmlFor={`${key}-base_url`}>
              API address
              <input
                required
                type="url"
                id={`${key}-base_url`}
                value={p.base_url}
                onChange={(e) => change(i, { base_url: e.target.value })}
                placeholder="https://openrouter.ai/api/v1"
              />
            </label>
            <label htmlFor={`${key}-api_key_env`}>
              API key variable
              <input
                id={`${key}-api_key_env`}
                value={p.api_key_env}
                onChange={(e) => change(i, { api_key_env: e.target.value })}
                pattern="[A-Za-z_][A-Za-z0-9_]*"
                autoComplete="off"
              />
              <span className="hint">
                The environment variable's name, never the key. Blank sends no
                key, which only an API on this machine accepts.
              </span>
            </label>
            <label htmlFor={`${key}-effort_parameter`}>
              Reasoning effort is sent as
              <select
                id={`${key}-effort_parameter`}
                value={
                  p.effort_parameter === "reasoning.effort"
                    ? "reasoning.effort"
                    : ""
                }
                onChange={(e) =>
                  change(i, {
                    effort_parameter: e.target.value as EffortParameter,
                  })
                }
              >
                <option value="">reasoning_effort</option>
                <option value="reasoning.effort">reasoning.effort</option>
              </select>
              <span className="hint">
                OpenAI and xAI read reasoning_effort; gateways such as Vercel AI
                Gateway and OpenRouter read reasoning.effort.
              </span>
            </label>
          </fieldset>
        );
      })}
    </>
  );
}

/**
 * How a CLI engine's folder is named and explained; one without its own
 * words is described plainly.
 */
const folders: Record<string, { label: string; hint: ReactNode }> = {
  codex: {
    label: "Codex folder",
    hint: (
      <>
        Codex keeps its settings, login and sessions here; use one without
        global AGENTS files. After changing it, sign in with{" "}
        <code>crew-assistant model login</code>.
      </>
    ),
  },
  claude: {
    label: "Claude settings folder",
    hint: "Uses your existing Claude login.",
  },
};

/** One CLI engine's program and the folder its login lives in. */
function CLISettings({
  choice,
  defaults,
  setting,
  change,
}: {
  choice: EngineChoice;
  defaults?: { bin?: string; home?: string };
  setting: (key: string) => string;
  change: (key: string, next: string) => void;
}) {
  const { engine, label } = choice;
  const folder = folders[engine] ?? {
    label: `${label} folder`,
    hint: `${label} keeps its settings and login here. Blank uses its own.`,
  };
  return (
    <>
      <label htmlFor={`engines-${engine}-bin`}>
        {label} program
        <input
          id={`engines-${engine}-bin`}
          value={setting("bin")}
          onChange={(e) => change("bin", e.target.value)}
          placeholder={defaults?.bin}
          autoComplete="off"
        />
      </label>
      <label htmlFor={`engines-${engine}-home`}>
        {folder.label}
        <input
          id={`engines-${engine}-home`}
          value={setting("home")}
          onChange={(e) => change("home", e.target.value)}
          placeholder={defaults?.home || undefined}
          autoComplete="off"
        />
        <span className="hint">{folder.hint}</span>
      </label>
    </>
  );
}

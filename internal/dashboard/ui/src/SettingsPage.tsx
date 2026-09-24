import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { AssistantSetup } from "./AssistantSetup";
import { ConnectionsSettings } from "./ConnectionsSettings";
import { ChatSettings } from "./ChatSettings";
import { ModelSettings } from "./ModelSettings";
import { appearanceOf, applyAppearance, type Appearance } from "./appearance";
import { href } from "./router";
import { ErrorNotice, Pill, humanStatus, useAction } from "./ui";
import {
  errorText,
  getConfig,
  putConfig,
  section,
  type Config,
  type State,
} from "./api";

const sections = [
  { id: "assistant", label: "Assistant" },
  { id: "appearance", label: "Appearance" },
  { id: "model", label: "Model" },
  { id: "chat", label: "Chat" },
  { id: "connections", label: "Connections" },
  { id: "limits", label: "Limits" },
  { id: "advanced", label: "Advanced" },
] as const;

type SectionID = (typeof sections)[number]["id"];

const appearances: { id: Appearance; label: string }[] = [
  { id: "system", label: "Match the system" },
  { id: "light", label: "Light" },
  { id: "dark", label: "Dark" },
];

export function Settings({
  state,
  refresh,
  section: requested,
}: {
  state: State;
  refresh: () => Promise<void>;
  section?: string;
}) {
  const current: SectionID =
    sections.find((s) => s.id === requested)?.id ?? "assistant";
  const [saved, setSaved] = useState<Config | null>(null);
  const [draft, setDraft] = useState<Config | null>(null);
  const [loadError, setLoadError] = useState("");
  const saving = useAction();
  useEffect(() => {
    let alive = true;
    getConfig()
      .then((value) => {
        if (!alive) return;
        setSaved(value);
        setDraft(value);
      })
      .catch((e) => {
        if (alive) setLoadError(errorText(e));
      });
    return () => {
      alive = false;
    };
  }, []);
  const dirty = !!draft && JSON.stringify(draft) !== JSON.stringify(saved);
  async function put(next: Config) {
    await putConfig(next);
    await refresh();
  }
  async function save(e: FormEvent) {
    e.preventDefault();
    if (!draft) return;
    await saving.run(async () => {
      await put(draft);
      setSaved(draft);
    });
  }
  // Appearance is applied and kept at once; it never waits on other edits.
  async function chooseAppearance(theme: Appearance) {
    if (!saved || !draft) return;
    applyAppearance(theme);
    saving.setError("");
    const next = { ...saved, assistant: { ...saved.assistant, theme } };
    try {
      await put(next);
      setSaved(next);
      setDraft({ ...draft, assistant: { ...draft.assistant, theme } });
    } catch (err) {
      applyAppearance(saved.assistant?.theme);
      saving.setError(errorText(err));
    }
  }
  return (
    <div className="page settings">
      <header className="page-header">
        <h1>Settings</h1>
      </header>
      <div className="settings-layout">
        <nav className="settings-nav" aria-label="Settings sections">
          {sections.map((s) => (
            <a
              key={s.id}
              className="nav-link"
              href={href({ page: "settings", section: s.id })}
              aria-current={current === s.id ? "page" : undefined}
            >
              {s.label}
            </a>
          ))}
        </nav>
        <form className="settings-body" onSubmit={save} aria-label="Settings">
          <ErrorNotice error={loadError || saving.error} />
          {!draft ? (
            !loadError && <p className="muted">Loading…</p>
          ) : (
            <>
              {current === "assistant" && (
                <AssistantSection
                  state={state}
                  config={draft}
                  onChange={setDraft}
                  onApplied={async () => {
                    const fresh = await getConfig();
                    setSaved(fresh);
                    setDraft(fresh);
                    await refresh();
                  }}
                />
              )}
              {current === "appearance" && (
                <Panel title="Appearance">
                  <div
                    className="segmented"
                    role="group"
                    aria-label="Appearance"
                  >
                    {appearances.map((a) => (
                      <button
                        key={a.id}
                        type="button"
                        aria-pressed={
                          appearanceOf(saved?.assistant?.theme) === a.id
                        }
                        onClick={() => void chooseAppearance(a.id)}
                      >
                        {a.label}
                      </button>
                    ))}
                  </div>
                </Panel>
              )}
              {current === "model" && (
                <Panel title="The assistant's model">
                  <ModelSettings config={draft} onChange={setDraft} />
                </Panel>
              )}
              {current === "chat" && (
                <Panel title="Chat">
                  <ChatSettings config={draft} onChange={setDraft} />
                </Panel>
              )}
              {current === "connections" && (
                <>
                  <ConnectionsSettings
                    connections={draft.connections || []}
                    onChange={(connections) =>
                      setDraft({ ...draft, connections })
                    }
                  />
                  {state.integrations.length > 0 && (
                    <Panel title="Status">
                      <ul className="rows">
                        {state.integrations.map((i) => (
                          <li key={i.id} className="integration">
                            <span>
                              <strong>{i.name}</strong>
                              {i.detail && (
                                <span className="muted small"> {i.detail}</span>
                              )}
                            </span>
                            <Pill
                              tone={
                                ["connected", "ready", "configured"].includes(
                                  i.status,
                                )
                                  ? "done"
                                  : "needs"
                              }
                            >
                              {humanStatus(i.status)}
                            </Pill>
                          </li>
                        ))}
                      </ul>
                    </Panel>
                  )}
                </>
              )}
              {current === "limits" && (
                <LimitsSection config={draft} onChange={setDraft} />
              )}
              {current === "advanced" && (
                <AdvancedSection config={draft} onChange={setDraft} />
              )}
            </>
          )}
          {dirty && (
            <div
              className="save-bar"
              role="region"
              aria-label="Unsaved changes"
            >
              <span>Unsaved changes</span>
              <div className="actions">
                <button
                  type="button"
                  className="btn btn-quiet"
                  disabled={saving.busy}
                  onClick={() => setDraft(saved)}
                >
                  Discard
                </button>
                <button
                  className="btn btn-primary"
                  disabled={saving.busy || !draft?.assistant?.name?.trim()}
                >
                  Save
                </button>
              </div>
            </div>
          )}
        </form>
      </div>
    </div>
  );
}

function Panel({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="tab-panel card settings-panel" aria-label={title}>
      <h2>{title}</h2>
      {children}
    </section>
  );
}

function AssistantSection({
  state,
  config,
  onChange,
  onApplied,
}: {
  state: State;
  config: Config;
  onChange: (next: Config) => void;
  onApplied: () => Promise<void>;
}) {
  const assistant = config.assistant ?? {};
  const set = (patch: Record<string, unknown>) =>
    onChange({ ...config, assistant: { ...assistant, ...patch } });
  return (
    <>
      <Panel title="Assistant">
        <label htmlFor="assistant-name">
          Name
          <input
            id="assistant-name"
            value={assistant.name ?? ""}
            onChange={(e) => set({ name: e.target.value })}
            maxLength={80}
            required
          />
        </label>
        <label htmlFor="personality">
          Personality
          <textarea
            id="personality"
            value={assistant.personality ?? ""}
            onChange={(e) => set({ personality: e.target.value })}
            rows={4}
            maxLength={10000}
            placeholder="Calm and direct. Bring a recommendation, not just a question."
          />
          <span className="hint">
            How it writes to you. It can't change what it's allowed to do.
          </span>
        </label>
      </Panel>
      <AssistantSetup
        currentName={state.assistant.name}
        demo={state.demo}
        onApplied={onApplied}
      />
    </>
  );
}

function numberOrEmpty(value: unknown) {
  return typeof value === "number" ? value : "";
}

const usageFields = [
  ["codex_max_used_percent", "Pause Codex teams at (% used)"],
  ["claude_max_used_percent", "Pause Claude teams at (% used)"],
] as const;

function LimitsSection({
  config,
  onChange,
}: {
  config: Config;
  onChange: (value: Config) => void;
}) {
  const limits = section(config.limits);
  const usage = section(limits.role_usage);
  const setLimit = (key: string, value: number) =>
    onChange({ ...config, limits: { ...limits, [key]: value } });
  const setUsage = (key: string, value: unknown) =>
    onChange({
      ...config,
      limits: { ...limits, role_usage: { ...usage, [key]: value } },
    });
  return (
    <>
      <Panel title="Assistant model calls">
        <div className="form-row">
          <label htmlFor="limits-max_model_calls_per_day">
            Per day
            <input
              id="limits-max_model_calls_per_day"
              type="number"
              min={1}
              max={100000}
              value={numberOrEmpty(limits.max_model_calls_per_day)}
              onChange={(e) =>
                setLimit("max_model_calls_per_day", Number(e.target.value))
              }
            />
          </label>
          <label htmlFor="limits-max_model_turns">
            Steps per chat reply
            <input
              id="limits-max_model_turns"
              type="number"
              min={1}
              max={32}
              value={numberOrEmpty(limits.max_model_turns)}
              onChange={(e) =>
                setLimit("max_model_turns", Number(e.target.value))
              }
            />
          </label>
        </div>
        <p className="hint">These count model calls, not money.</p>
      </Panel>
      <Panel title="Subscription use">
        <div className="form-row">
          {usageFields.map(([key, label]) => (
            <label key={key} htmlFor={`role-usage-${key}`}>
              {label}
              <input
                id={`role-usage-${key}`}
                type="number"
                min={0}
                max={100}
                value={numberOrEmpty(usage[key])}
                onChange={(e) => setUsage(key, Number(e.target.value))}
              />
            </label>
          ))}
          <label htmlFor="role-usage-unavailable">
            If use can't be checked
            <select
              id="role-usage-unavailable"
              value={usage.on_unavailable === "pause" ? "pause" : "allow"}
              onChange={(e) => setUsage("on_unavailable", e.target.value)}
            >
              <option value="allow">Carry on</option>
              <option value="pause">Wait until it can be</option>
            </select>
          </label>
        </div>
        <p className="hint">
          A paused team carries on by itself when its usage window resets. 0
          never pauses.
        </p>
      </Panel>
    </>
  );
}

function AdvancedSection({
  config,
  onChange,
}: {
  config: Config;
  onChange: (value: Config) => void;
}) {
  const [listDrafts, setListDrafts] = useState<Record<string, string>>({});
  const linear = section(config.linear);
  function field(
    group: string,
    key: string,
    label: string,
    options: { hint?: string; env?: boolean; list?: boolean } = {},
  ) {
    const object = section(config[group]);
    const raw = object[key];
    const value =
      options.list && listDrafts[`${group}.${key}`] !== undefined
        ? listDrafts[`${group}.${key}`]
        : Array.isArray(raw)
          ? raw.join(", ")
          : typeof raw === "string"
            ? raw
            : "";
    return (
      <label key={`${group}.${key}`} htmlFor={`${group}-${key}`}>
        {label}
        <input
          id={`${group}-${key}`}
          value={value}
          pattern={options.env ? "[A-Za-z_][A-Za-z0-9_]*" : undefined}
          autoComplete="off"
          onChange={(e) => {
            const text = e.target.value;
            if (options.list)
              setListDrafts({ ...listDrafts, [`${group}.${key}`]: text });
            onChange({
              ...config,
              [group]: {
                ...object,
                [key]: options.list
                  ? text
                      .split(",")
                      .map((x) => x.trim())
                      .filter(Boolean)
                  : text,
              },
            });
          }}
        />
        {options.hint && <span className="hint">{options.hint}</span>}
      </label>
    );
  }
  return (
    <>
      <p className="muted">
        For a Slack bot of its own, or reading Linear directly. Connections are
        the simpler way. Changes here need crew-assistant restarted.
      </p>
      <Panel title="Slack bot">
        {field("slack", "owner_user_id", "Your Slack user ID")}
        {field("slack", "bot_token_env", "Bot token variable", {
          env: true,
          hint: "The environment variable's name, never the token.",
        })}
        {field("slack", "app_token_env", "App token variable", { env: true })}
      </Panel>
      <Panel title="Linear">
        <label className="check">
          <input
            type="checkbox"
            checked={linear.import_assignments === true}
            onChange={(e) =>
              onChange({
                ...config,
                linear: { ...linear, import_assignments: e.target.checked },
              })
            }
          />
          <span>Add issues assigned to you as projects</span>
        </label>
        {field("linear", "api_key_env", "API key variable", { env: true })}
        {field("linear", "team_ids", "Team IDs", {
          list: true,
          hint: "Separate with commas. Only these teams are read.",
        })}
      </Panel>
    </>
  );
}

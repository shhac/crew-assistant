import { useEffect, useState, type FormEvent } from "react";
import { AssistantSetup } from "./AssistantSetup";
import { ConnectionsSettings } from "./ConnectionsSettings";
import { ChatSettings } from "./ChatSettings";
import { ModelSettings } from "./ModelSettings";
import { LimitsSettings } from "./LimitsSettings";
import { AdvancedSettings } from "./AdvancedSettings";
import { Panel } from "./SettingsPanel";
import { appearanceOf, applyAppearance, type Appearance } from "./appearance";
import { href } from "./router";
import { LookForm } from "./Redraw";
import { Avatar } from "./Avatar";
import { ErrorNotice, Pill, humanStatus, useAction } from "./ui";
import {
  errorText,
  getConfig,
  putConfig,
  redrawAssistant,
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
                  refresh={refresh}
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
                <LimitsSettings config={draft} onChange={setDraft} />
              )}
              {current === "advanced" && (
                <AdvancedSettings config={draft} onChange={setDraft} />
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

function AssistantSection({
  state,
  refresh,
  config,
  onChange,
  onApplied,
}: {
  state: State;
  refresh: () => Promise<void>;
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
        <div className="face-edit">
          <Avatar of={state.assistant} size={96} />
          <LookForm
            key={state.assistant.avatar?.look ?? ""}
            id="assistant-look"
            face={state.assistant}
            onRedraw={async (look) => {
              await redrawAssistant(look);
              await refresh();
            }}
          />
        </div>
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

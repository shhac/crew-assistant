import { Panel } from "./SettingsPanel";
import {
  section,
  withEngine,
  withSettings,
  type Config,
  type ConfigDefaults,
} from "./api";

function numberOrEmpty(value: unknown) {
  return typeof value === "number" ? value : "";
}

const subscriptions = [
  { engine: "codex", label: "Codex" },
  { engine: "claude", label: "Claude" },
] as const;

const windows = [
  ["5h_percent", "Keep unused of the 5-hour window (%)"],
  ["1w_percent", "Keep unused of the weekly window (%)"],
] as const;

export function LimitsSettings({
  config,
  defaults,
  onChange,
}: {
  config: Config;
  defaults?: ConfigDefaults;
  onChange: (value: Config) => void;
}) {
  const limits = section(config.limits);
  const setLimit = (key: string, value: number) =>
    onChange({ ...config, limits: { ...limits, [key]: value } });
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
      {subscriptions.map(({ engine, label }) => (
        <SubscriptionUse
          key={engine}
          engine={engine}
          label={label}
          config={config}
          defaults={defaults}
          onChange={onChange}
        />
      ))}
    </>
  );
}

function SubscriptionUse({
  engine,
  label,
  config,
  defaults,
  onChange,
}: {
  engine: string;
  label: string;
  config: Config;
  defaults?: ConfigDefaults;
  onChange: (value: Config) => void;
}) {
  const settings = section(section(config.engines)[engine]);
  const floor = section(settings.usage_floor);
  const setFloor = (key: string, input: string) => {
    const next = withSettings(floor, {
      [key]: input === "" ? undefined : Number(input),
    });
    onChange(
      withEngine(config, engine, {
        usage_floor: Object.keys(next).length ? next : undefined,
      }),
    );
  };
  const onUnknown =
    settings.on_unknown_usage ?? defaults?.on_unknown_usage ?? "allow";
  return (
    <Panel title={`${label} subscription`}>
      <div className="form-row">
        {windows.map(([key, text]) => (
          <label key={key} htmlFor={`engines-${engine}-${key}`}>
            {text}
            <input
              id={`engines-${engine}-${key}`}
              type="number"
              min={0}
              max={100}
              step={1}
              value={numberOrEmpty(floor[key])}
              placeholder={defaults?.usage_floor?.toString()}
              onChange={(e) => setFloor(key, e.target.value)}
            />
          </label>
        ))}
        <label htmlFor={`engines-${engine}-on_unknown_usage`}>
          If use can't be read
          <select
            id={`engines-${engine}-on_unknown_usage`}
            value={onUnknown === "pause" ? "pause" : "allow"}
            onChange={(e) =>
              onChange(
                withEngine(config, engine, {
                  on_unknown_usage: e.target.value,
                }),
              )
            }
          >
            <option value="allow">Carry on</option>
            <option value="pause">Wait until it can be</option>
          </select>
        </label>
      </div>
      <p className="hint">
        {label} teams wait while less than this is left, and carry on when the
        window resets. Blank uses the default; 0 never waits.
      </p>
    </Panel>
  );
}

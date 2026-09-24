import { Panel } from "./SettingsPanel";
import { section, type Config } from "./api";

function numberOrEmpty(value: unknown) {
  return typeof value === "number" ? value : "";
}

const usageFields = [
  ["codex_max_used_percent", "Pause Codex teams at (% used)"],
  ["claude_max_used_percent", "Pause Claude teams at (% used)"],
] as const;

export function LimitsSettings({
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

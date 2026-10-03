import type { Config, UpdateStatus } from "./api";
import { Panel } from "./SettingsPanel";
import { ErrorNotice } from "./ui";

export function UpdatesSettings({
  config,
  update,
  onChange,
}: {
  config: Config;
  update?: UpdateStatus;
  onChange: (value: Config) => void;
}) {
  return (
    <Panel title="Updates">
      <div className="form">
        <label>
          <span>Update notices</span>
          <select
            className="field"
            value={config.upgrade?.mode ?? "ask"}
            onChange={(e) =>
              onChange({
                ...config,
                upgrade: {
                  ...config.upgrade,
                  mode: e.target.value as "off" | "ask",
                },
              })
            }
          >
            <option value="off">Off</option>
            <option value="ask">Ask me</option>
          </select>
        </label>
      </div>
      <p className="hint">
        Ask me opens a decision with release notes and instructions to upgrade
        by hand. Off keeps checking and shows available versions here.
      </p>
      {update?.running && <p>Running {versionLabel(update.running)}</p>}
      {update?.available && (
        <p>
          {versionLabel(update.available)} available
          {update.skipped === update.available ? " (skipped)" : ""}
        </p>
      )}
      {update?.checked_at && (
        <p className="hint">
          Last checked {new Date(update.checked_at).toLocaleString()}
        </p>
      )}
      {update?.unavailable && <p className="hint">{update.unavailable}</p>}
      <ErrorNotice error={update?.error ?? ""} />
    </Panel>
  );
}

export function versionLabel(version: string) {
  return /^v?\d+\.\d+\.\d+/.test(version)
    ? `v${version.replace(/^v/, "")}`
    : version;
}

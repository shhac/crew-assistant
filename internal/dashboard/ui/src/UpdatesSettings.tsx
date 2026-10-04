import type { Config, UpdateStatus, RollbackStatus } from "./api";
import { Panel } from "./SettingsPanel";
import { ErrorNotice } from "./ui";

export function UpdatesSettings({
  config,
  update,
  rollback,
  onChange,
}: {
  config: Config;
  update?: UpdateStatus;
  rollback?: RollbackStatus;
  onChange: (value: Config) => void;
}) {
  return (
    <Panel title="Updates">
      <RollbackNotice rollback={rollback} />
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
                  mode: e.target.value as "off" | "ask" | "automatic",
                },
              })
            }
          >
            <option value="off">Off</option>
            <option value="ask">Ask me</option>
            <option value="automatic">Automatic</option>
          </select>
        </label>
      </div>
      <p className="hint">
        Automatic waits for a quiet moment with no running replies or steps, up
        to a 6-hour cap, then finishes running work before installing. Ask me
        opens a decision with release notes and an upgrade choice for Homebrew
        installs. You can also upgrade by hand. Off keeps checking and shows
        available versions here.
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

export function RollbackNotice({ rollback }: { rollback?: RollbackStatus }) {
  if (!rollback) return null;
  return (
    <div className="banner banner-alert" role="status">
      <p>
        Rollback is in force. Restored {versionLabel(rollback.from)} after the
        upgrade to {versionLabel(rollback.to)} failed: {rollback.failure}
      </p>
      <p>
        To clear it, run <code>{rollback.clear}</code>. This takes fresh backups
        and retries the upgrade. Keep any custom state/config flags from your
        daemon command.
      </p>
    </div>
  );
}

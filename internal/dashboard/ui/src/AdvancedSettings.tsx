import { useState } from "react";
import { Panel } from "./SettingsPanel";
import { section, type Config } from "./api";

export function AdvancedSettings({
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
        Read Linear directly. You can also use a CLI account in Connections.
        Changes here need crew-assistant restarted.
      </p>
      <Panel title="Decision record">
        <p>
          Answered decisions, kept for evaluating a future decision classifier.
        </p>
        <a href="/api/decisions/evaluations.jsonl" download>
          Download JSONL
        </a>
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

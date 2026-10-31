import { choicesFor, engineLabel, useEngineChoices } from "./engines";

/** Whether a member on an engine can be allowed its CLI's own subagents. */
export function useSubagentsOffered(engine: string) {
  return choicesFor(useEngineChoices(), "subagents").some(
    (c) => c.engine === engine,
  );
}

/**
 * Whether a member's task turns may split their work across their CLI's own
 * subagents. It is offered only on an engine whose sandboxed sessions can
 * start them; one left on elsewhere can only be switched off.
 */
export function SubagentsField({
  engine,
  value,
  onChange,
}: {
  engine: string;
  value: boolean;
  onChange: (on: boolean) => void;
}) {
  const offered = useSubagentsOffered(engine);
  if (!offered && !value) return null;
  return (
    <fieldset className="subagents-setting">
      <legend>Subagents</legend>
      <label className="check">
        <input
          type="checkbox"
          checked={value}
          onChange={(e) => onChange(e.target.checked)}
        />
        <span>Allow subagents</span>
      </label>
      {offered ? (
        <p className="hint">
          Lets this member split its own work across its CLI's subagents, under
          the same sandbox. Uses more of your subscription.
        </p>
      ) : (
        <p className="member-kinds-problem">
          {engineLabel(engine)} can't use subagents. Switch them off to save.
        </p>
      )}
    </fieldset>
  );
}

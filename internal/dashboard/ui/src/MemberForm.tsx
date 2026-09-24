import { useState, type FormEvent } from "react";
import { engines, memberKinds } from "./members";
import { ErrorNotice, useAction } from "./ui";
import { saveMember, type Member, type MemberKind } from "./api";

export function MemberForm({
  member,
  onSaved,
  onCancel,
}: {
  member?: Member;
  onSaved: (member: Member) => Promise<void>;
  onCancel: () => void;
}) {
  const [name, setName] = useState(member?.name ?? "");
  const [kind, setKind] = useState<MemberKind>(member?.kind ?? "implementer");
  const [engine, setEngine] = useState(member?.engine ?? "claude");
  const [model, setModel] = useState(member?.model ?? "");
  const [effort, setEffort] = useState(member?.effort ?? "");
  const [instructions, setInstructions] = useState(member?.instructions ?? "");
  const { busy, error, run } = useAction();
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      const saved = await saveMember(member?.id ?? "", {
        name: name.trim(),
        kind,
        engine,
        model: model.trim(),
        effort: effort.trim(),
        instructions: instructions.trim(),
      });
      await onSaved(saved);
    });
  }
  return (
    <form
      className="form"
      onSubmit={save}
      aria-label={member ? `Edit ${member.name}` : "New member"}
    >
      <h2>{member ? `Edit ${member.name}` : "New member"}</h2>
      <label htmlFor="member-name">
        Name
        <input
          id="member-name"
          className="field"
          value={name}
          maxLength={40}
          onChange={(e) => setName(e.target.value)}
          required
        />
      </label>
      <div className="form-row">
        <label htmlFor="member-kind">
          Kind
          <select
            id="member-kind"
            className="field"
            value={kind}
            onChange={(e) =>
              setKind(
                memberKinds.find((k) => k.id === e.target.value)?.id ?? kind,
              )
            }
          >
            {memberKinds.map((k) => (
              <option key={k.id} value={k.id}>
                {k.label}
              </option>
            ))}
          </select>
        </label>
        <label htmlFor="member-engine">
          Engine
          <select
            id="member-engine"
            className="field"
            value={engine}
            onChange={(e) => setEngine(e.target.value)}
          >
            {engines.map((option) => (
              <option key={option.id} value={option.id}>
                {option.label}
              </option>
            ))}
          </select>
        </label>
        <label htmlFor="member-model">
          Model
          <input
            id="member-model"
            className="field"
            value={model}
            maxLength={80}
            onChange={(e) => setModel(e.target.value)}
          />
          <span className="hint">
            Optional. Empty uses the engine's default.
          </span>
        </label>
        <label htmlFor="member-effort">
          Reasoning effort
          <input
            id="member-effort"
            className="field"
            value={effort}
            maxLength={20}
            placeholder="high"
            onChange={(e) => setEffort(e.target.value)}
          />
          <span className="hint">Optional.</span>
        </label>
      </div>
      <label htmlFor="member-instructions">
        Instructions
        <textarea
          id="member-instructions"
          className="field"
          rows={5}
          value={instructions}
          maxLength={4000}
          onChange={(e) => setInstructions(e.target.value)}
        />
        <span className="hint">
          Added after the project's own instructions for this kind of work.
        </span>
      </label>
      <ErrorNotice error={error} />
      <div className="actions">
        <button
          className="btn btn-primary"
          type="submit"
          disabled={busy || !name.trim()}
        >
          {member ? "Save" : "Add member"}
        </button>
        <button
          className="btn btn-quiet"
          type="button"
          disabled={busy}
          onClick={onCancel}
        >
          Cancel
        </button>
      </div>
    </form>
  );
}

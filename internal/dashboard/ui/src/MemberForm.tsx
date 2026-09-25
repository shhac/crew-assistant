import { useState, type FormEvent } from "react";
import { engines, kindsProblem, memberKinds } from "./members";
import { modelLabel, useModelCatalog } from "./modelCatalog";
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
  const [kinds, setKinds] = useState<MemberKind[]>(
    member?.kinds ?? ["implementer"],
  );
  const problem = kindsProblem(kinds);
  const toggle = (kind: MemberKind, on: boolean) =>
    setKinds((current) =>
      memberKinds
        .map((k) => k.id)
        .filter((k) => (k === kind ? on : current.includes(k))),
    );
  const [engine, setEngine] = useState(member?.engine ?? "claude");
  const [model, setModel] = useState(member?.model ?? "");
  const [effort, setEffort] = useState(member?.effort ?? "");
  const [instructions, setInstructions] = useState(member?.instructions ?? "");
  const [description, setDescription] = useState(member?.description ?? "");
  const models = useModelCatalog(engine);
  const listed = models.options.some((option) => option.id === model);
  // Another engine's models don't run here; going back to the saved engine
  // brings back the saved model.
  const chooseEngine = (next: string) => {
    setEngine(next);
    setModel(next === member?.engine ? (member.model ?? "") : "");
  };
  const { busy, error, run } = useAction();
  async function save(e: FormEvent) {
    e.preventDefault();
    if (problem) return;
    await run(async () => {
      const saved = await saveMember(member?.id ?? "", {
        name: name.trim(),
        kinds,
        engine,
        model: model.trim(),
        effort: effort.trim(),
        instructions: instructions.trim(),
        description: description.trim(),
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
      <fieldset
        className="member-kinds"
        aria-describedby={problem ? "member-kinds-problem" : undefined}
      >
        <legend>Roles</legend>
        <div className="member-kinds-choices">
          {memberKinds.map((k) => (
            <label key={k.id} className="check">
              <input
                type="checkbox"
                checked={kinds.includes(k.id)}
                onChange={(e) => toggle(k.id, e.target.checked)}
              />
              <span>{k.label}</span>
            </label>
          ))}
        </div>
        {problem && (
          <p className="member-kinds-problem" id="member-kinds-problem">
            {problem}
          </p>
        )}
      </fieldset>
      <div className="form-row">
        <label htmlFor="member-engine">
          Engine
          <select
            id="member-engine"
            className="field"
            value={engine}
            onChange={(e) => chooseEngine(e.target.value)}
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
          <select
            id="member-model"
            className="field"
            value={model}
            onChange={(e) => setModel(e.target.value)}
            disabled={models.loading}
          >
            <option value="">The engine's default</option>
            {model && !listed && <option value={model}>{model} (saved)</option>}
            {models.options.map((option) => (
              <option key={option.id} value={option.id}>
                {modelLabel(option, models.catalog)}
              </option>
            ))}
          </select>
          <span className="hint" role="status">
            {models.loading
              ? "Finding models…"
              : models.error || models.catalog?.detail}
          </span>
          {models.catalog?.available && model && !listed && (
            <span className="hint">
              The saved model isn't in this list. It stays until you pick
              another.
            </span>
          )}
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
      <div className="actions">
        <button
          className="btn btn-sm"
          type="button"
          disabled={models.loading}
          onClick={models.refresh}
        >
          Refresh the list of models
        </button>
      </div>
      <label htmlFor="member-description">
        Description
        <textarea
          id="member-description"
          className="field"
          rows={3}
          value={description}
          maxLength={1000}
          onChange={(e) => setDescription(e.target.value)}
        />
        <span className="hint">
          Optional. Who they are, in your words; every picture of them is drawn
          from it.
        </span>
      </label>
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
          disabled={busy || !name.trim() || !!problem}
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

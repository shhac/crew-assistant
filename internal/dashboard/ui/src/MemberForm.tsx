import { useEffect, useState, type FormEvent } from "react";
import { choicesFor, useEngineChoices } from "./engines";
import { kindsProblem, memberKinds } from "./members";
import { ModelFields } from "./ModelFields";
import { PersonalityField, SuggestedLook } from "./ProfileFields";
import type { Suggestion } from "./SuggestIdentity";
import { ErrorNotice, useAction } from "./ui";
import {
  BrowserFields,
  savedBrowser,
  useBrowserOffered,
} from "./BrowserFields";
import { saveMember, type Browser, type Member, type MemberKind } from "./api";

export function MemberForm({
  member,
  suggestion,
  onSaved,
  onCancel,
}: {
  member?: Member;
  /** A suggestion the owner chose to fill in a new member with. */
  suggestion?: Suggestion;
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
  const [picked, setChoice] = useState({
    engine: member?.engine ?? "",
    model: member?.model ?? "",
    effort: member?.effort ?? "",
  });
  // A new member starts on Claude while it can run a role, and otherwise on
  // the first engine that can.
  const roleEngines = choicesFor(useEngineChoices(), "roles").map(
    (c) => c.engine,
  );
  const firstEngine = roleEngines.includes("claude")
    ? "claude"
    : (roleEngines[0] ?? "");
  const choice = { ...picked, engine: picked.engine || firstEngine };
  const [instructions, setInstructions] = useState(member?.instructions ?? "");
  const [description, setDescription] = useState(member?.description ?? "");
  const [personality, setPersonality] = useState(member?.personality ?? "");
  const [browser, setBrowser] = useState<Browser>(member?.browser ?? {});
  // Any member may be allowed the browser, on an engine whose roles can use
  // one.
  const browserOffered = useBrowserOffered(choice.engine);
  const browserOn = !!browser.on;
  const browserProblem = browserOn && !browserOffered;
  useEffect(() => {
    if (!suggestion) return;
    setName(suggestion.name);
    setPersonality(suggestion.personality);
  }, [suggestion]);
  const { busy, error, run } = useAction();
  async function save(e: FormEvent) {
    e.preventDefault();
    if (problem) return;
    await run(async () => {
      const saved = await saveMember(member?.id ?? "", {
        name: name.trim(),
        kinds,
        engine: choice.engine,
        model: choice.model.trim(),
        effort: choice.effort.trim(),
        instructions: instructions.trim(),
        description: description.trim(),
        personality: personality.trim(),
        browser: savedBrowser(browser),
        ...(!member && suggestion ? { avatar: suggestion.avatar } : {}),
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
      <ModelFields
        id="member"
        use="roles"
        value={choice}
        saved={member}
        onChange={setChoice}
      />
      <BrowserFields
        id="member"
        engine={choice.engine}
        value={browser}
        onChange={setBrowser}
        purpose="member"
      />
      <PersonalityField
        id="member-personality"
        value={personality}
        onChange={setPersonality}
      />
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
      {!member && suggestion && <SuggestedLook suggestion={suggestion} />}
      <ErrorNotice error={error} />
      <div className="actions">
        <button
          className="btn btn-primary"
          type="submit"
          disabled={
            busy ||
            !name.trim() ||
            !!problem ||
            !choice.engine ||
            browserProblem
          }
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

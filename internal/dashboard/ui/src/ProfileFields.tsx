import { Avatar } from "./Avatar";
import type { Suggestion } from "./SuggestIdentity";

/** How someone on the team writes: an assistant or a member. */
export function PersonalityField({
  id,
  value,
  onChange,
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <label htmlFor={id}>
      Personality
      <textarea
        id={id}
        className="field"
        rows={4}
        value={value}
        maxLength={4000}
        placeholder="Calm and direct. Bring a recommendation, not just a question."
        onChange={(e) => onChange(e.target.value)}
      />
      <span className="hint">
        How they write. It can't change what they're allowed to do.
      </span>
    </label>
  );
}

/** The face a suggestion sketched, and the look Codex draws once they're added. */
export function SuggestedLook({ suggestion }: { suggestion: Suggestion }) {
  return (
    <div className="face-edit">
      <Avatar of={suggestion} size={48} />
      <p className="soft small">
        {suggestion.avatar.look
          ? `Codex draws them once they're added: ${suggestion.avatar.look}`
          : "Codex draws them once they're added."}
      </p>
    </div>
  );
}

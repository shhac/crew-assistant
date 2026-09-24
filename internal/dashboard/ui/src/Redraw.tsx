import { useState } from "react";
import { ErrorNotice, useAction } from "./ui";
import type { AvatarSpec } from "./api";

/** Anyone Codex draws: the assistant or a member. */
export interface Drawable {
  avatar?: AvatarSpec;
  drawing?: boolean;
  draw_error?: string;
}

export function DrawingStatus({ face }: { face: Drawable }) {
  if (face.drawing)
    return (
      <p className="muted small" role="status">
        Drawing… this takes a few minutes.
      </p>
    );
  return <ErrorNotice error={face.draw_error ?? ""} />;
}

/**
 * How the picture should look, and a button to draw it again. It is not a
 * form of its own, since it can sit inside the settings form.
 */
export function LookForm({
  id,
  face,
  onRedraw,
  onCancel,
}: {
  id: string;
  face: Drawable;
  onRedraw: (look: string) => Promise<unknown>;
  onCancel?: () => void;
}) {
  const [look, setLook] = useState(face.avatar?.look ?? "");
  const { busy, error, run } = useAction();
  return (
    <div className="form look-form">
      <label htmlFor={id}>
        Look
        <textarea
          id={id}
          className="field"
          rows={2}
          maxLength={600}
          value={look}
          onChange={(e) => setLook(e.target.value)}
        />
        <span className="hint">Leave it as it is to redraw the same look.</span>
      </label>
      <ErrorNotice error={error} />
      <DrawingStatus face={face} />
      {!face.drawing && (
        <div className="actions">
          <button
            type="button"
            className="btn"
            disabled={busy}
            onClick={() => void run(() => onRedraw(look.trim()))}
          >
            Redraw
          </button>
          {onCancel && (
            <button
              type="button"
              className="btn btn-quiet"
              disabled={busy}
              onClick={onCancel}
            >
              Cancel
            </button>
          )}
        </div>
      )}
    </div>
  );
}

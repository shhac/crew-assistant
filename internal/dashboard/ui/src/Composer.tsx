import { type FormEvent } from "react";
import { Icon } from "./ui";
import {
  sizeLabel,
  type ComposerAsset,
  type useFileDrop,
} from "./composerAssets";

type Props = ReturnType<typeof useFileDrop> & {
  value: string;
  onChange: (value: string) => void;
  assets: ComposerAsset[];
  onRemove: (id: string) => void;
  reading: number;
  suggestion: string;
  onSubmit: (event: FormEvent) => void;
  name: string;
  large?: boolean;
  showHint?: boolean;
  onExpand: () => void;
};

/** Both editing spaces use the same draft and keyboard behavior. */
export function Composer({
  value,
  onChange,
  assets,
  onRemove,
  reading,
  suggestion,
  onSubmit,
  name,
  large,
  showHint,
  onExpand,
  dragging,
  dropHandlers,
  onPaste,
}: Props) {
  return (
    <form
      className={`composer${large ? " composer-large" : ""}${dragging ? " dragging" : ""}`}
      onSubmit={onSubmit}
      {...dropHandlers}
    >
      <label className="sr-only" htmlFor="chat-message">
        Message {name}
      </label>
      {(assets.length > 0 || reading > 0) && (
        <ul className="attachments" aria-label="Attachments">
          {assets.map((asset) => (
            <li key={asset.id}>
              <span className="attachment-name">{asset.name}</span>
              <span className="muted small">{sizeLabel(asset.size)}</span>
              <button
                type="button"
                className="btn btn-quiet btn-icon btn-sm"
                aria-label={`Remove attachment ${asset.name}`}
                onClick={() => onRemove(asset.id)}
              >
                <Icon name="Close" size={12} />
              </button>
            </li>
          ))}
          {reading > 0 && <li className="muted small">Reading files…</li>}
        </ul>
      )}
      <div className="composer-row">
        <textarea
          id="chat-message"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          onPaste={onPaste}
          // A suggestion is shown, never committed: the draft stays empty
          // and Send stays disabled until the owner takes it.
          placeholder={suggestion || `Message ${name}`}
          className={suggestion ? "has-suggestion" : undefined}
          rows={2}
          maxLength={20000}
          onKeyDown={(e) => {
            if (
              e.key === "Tab" &&
              suggestion &&
              !e.shiftKey &&
              !e.altKey &&
              !e.ctrlKey &&
              !e.metaKey
            ) {
              // Accepting makes it an ordinary draft to edit; it is not sent.
              e.preventDefault();
              onChange(suggestion);
              return;
            }
            if (
              e.key === "Enter" &&
              !e.shiftKey &&
              !e.nativeEvent.isComposing &&
              e.keyCode !== 229
            ) {
              e.preventDefault();
              e.currentTarget.form?.requestSubmit();
            }
          }}
        />
        {!large && (
          <button
            type="button"
            className="btn btn-quiet btn-icon"
            aria-label="Write in a larger space"
            title="Write in a larger space"
            onClick={onExpand}
          >
            <Icon name="WriteLarger" />
          </button>
        )}
        <button
          className="btn btn-primary btn-icon"
          type="submit"
          disabled={(!value.trim() && !assets.length) || reading > 0}
          aria-label="Send"
        >
          <Icon name="Send" size={15} />
        </button>
      </div>
      <p className="composer-hint">
        {suggestion ? (
          <>
            <span className="kbd">Tab</span> takes the suggestion
          </>
        ) : !large && !showHint ? null : (
          <>
            <span className="kbd">Enter</span> sends ·{" "}
            <span className="kbd">Shift Enter</span> new line · drop text files
            to attach
          </>
        )}
      </p>
    </form>
  );
}

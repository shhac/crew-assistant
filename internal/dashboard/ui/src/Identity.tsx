import type { CSSProperties } from "react";

export type Theme = "graphite-sage" | "ink-blue" | "charcoal-amber";
export interface AvatarSpec {
  shape?: "orb" | "spark" | "leaf";
  background?: string;
  accent?: string;
}
export const themes: {
  id: Theme;
  name: string;
  description: string;
  colors: string[];
}[] = [
  {
    id: "graphite-sage",
    name: "Graphite + sage",
    description: "Quiet neutrals, a little warmth.",
    colors: ["#1b1d20", "#a9c5b2"],
  },
  {
    id: "ink-blue",
    name: "Ink + soft blue",
    description: "Cool, clear and focused.",
    colors: ["#151b25", "#a9c5e8"],
  },
  {
    id: "charcoal-amber",
    name: "Charcoal + amber",
    description: "Soft contrast, a warmer glow.",
    colors: ["#211f1d", "#e4c292"],
  },
];
function isTheme(value?: string): value is Theme {
  return themes.some((t) => t.id === value);
}
export function validTheme(value?: string): Theme {
  return isTheme(value) ? value : "graphite-sage";
}
function safeColor(value: string | undefined, fallback: string) {
  return value && /^#[0-9a-f]{6}$/i.test(value) ? value : fallback;
}
export function Avatar({
  avatar,
  small = false,
}: {
  avatar?: AvatarSpec;
  small?: boolean;
}) {
  const shape = avatar?.shape || "orb";
  return (
    <span
      className={`identity-avatar ${small ? "small" : ""}`}
      style={
        {
          "--avatar-bg": safeColor(avatar?.background, "#252c29"),
          "--avatar-accent": safeColor(avatar?.accent, "#b4cdbd"),
        } as CSSProperties
      }
      aria-hidden="true"
    >
      <svg viewBox="0 0 48 48" fill="none">
        {shape === "spark" ? (
          <>
            <path
              d="M24 8l4.3 11.7L40 24l-11.7 4.3L24 40l-4.3-11.7L8 24l11.7-4.3L24 8z"
              fill="currentColor"
            />
            <circle cx="36" cy="12" r="2" fill="currentColor" opacity=".45" />
          </>
        ) : shape === "leaf" ? (
          <>
            <path
              d="M35 11c-18-1-26 8-23 19 3 10 22 7 23-19z"
              fill="currentColor"
              opacity=".85"
            />
            <path
              d="M14 34l16-17"
              stroke="var(--avatar-bg)"
              strokeWidth="2"
              strokeLinecap="round"
            />
          </>
        ) : (
          <>
            <circle cx="24" cy="24" r="11" fill="currentColor" opacity=".92" />
            <ellipse
              cx="24"
              cy="24"
              rx="20"
              ry="7"
              transform="rotate(-32 24 24)"
              stroke="currentColor"
              strokeWidth="1.4"
            />
            <circle cx="37" cy="11" r="2.5" fill="currentColor" />
          </>
        )}
      </svg>
    </span>
  );
}
export function Waiting({ label, detail }: { label: string; detail?: string }) {
  return (
    <div className="waiting-indicator" role="status">
      <span className="waiting-orbit" aria-hidden="true">
        <i />
        <i />
        <i />
      </span>
      <div>
        <strong>{label}</strong>
        {detail && <span>{detail}</span>}
      </div>
    </div>
  );
}
export function ThemePicker({
  value,
  onChange,
}: {
  value?: string;
  onChange: (theme: Theme) => void;
}) {
  return (
    <fieldset className="theme-picker">
      <legend>Workspace palette</legend>
      <div className="theme-options">
        {themes.map((theme) => (
          <button
            key={theme.id}
            type="button"
            className={`theme-option ${validTheme(value) === theme.id ? "selected" : ""}`}
            aria-pressed={validTheme(value) === theme.id}
            onClick={() => onChange(theme.id)}
          >
            <span
              className="theme-swatch"
              aria-hidden="true"
              style={{ background: theme.colors[0] }}
            >
              <i style={{ background: theme.colors[1] }} />
            </span>
            <strong>{theme.name}</strong>
            <small>{theme.description}</small>
          </button>
        ))}
      </div>
      <p className="field-hint">
        All palettes are designed for dark mode. Saved with your assistant.
      </p>
    </fieldset>
  );
}

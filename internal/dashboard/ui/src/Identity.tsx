import type { CSSProperties } from "react";

export interface AvatarSpec {
  shape?: "orb" | "spark" | "leaf";
  background?: string;
  accent?: string;
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

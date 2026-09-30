import { engineLabel, useEngineChoices } from "./engines";
// Original provider marks supplied by Misha in CA-36 design 1.
const glyphs = {
  claude:
    "M17.8 6.3A7.5 7.5 0 1 0 17.8 17.7M18.5 8.2l2-1.2M19.1 12h2.4M18.5 15.8l2 1.2",
  codex:
    "M12 3.5l4 2.3v4.6l4 2.3v4.6l-4 2.3-4-2.3-4 2.3-4-2.3v-4.6l4-2.3V5.8zM8 5.8l4 2.3 4-2.3M4 12.7l4 2.3v4.6M20 12.7l-4 2.3v4.6",
  grok: "M5 18.5L18.5 5M6 6h7l-7 7zM18 11v7h-7",
  api: "M9 3.5v5M15 3.5v5M6.5 8.5h11v2.3a5.5 5.5 0 0 1-11 0zM12 16.3v4.2",
};
export const providerGlyph = (engine: string) =>
  engine === "claude" || engine === "codex" || engine === "grok"
    ? glyphs[engine]
    : glyphs.api;
export function ProviderIcon({
  engine,
  label,
  size = 14,
}: {
  engine: string;
  label?: string;
  size?: number;
}) {
  const choices = useEngineChoices(false);
  const name = label ?? engineLabel(engine, choices);
  return (
    <span className="provider-icon" role="img" aria-label={name} title={name}>
      <svg
        aria-hidden="true"
        width={size}
        height={size}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="round"
        strokeLinejoin="round"
      >
        <path d={providerGlyph(engine)} />
      </svg>
    </span>
  );
}

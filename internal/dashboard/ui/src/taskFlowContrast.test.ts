import { describe, expect, it } from "vitest";
import baseCSS from "./styles/base.css?raw";
import requestCSS from "./styles/request.css?raw";
import tokenCSS from "./styles/tokens.css?raw";

function declarations(css: string, selector: string) {
  const start = css.indexOf(`${selector} {`);
  if (start < 0) throw new Error(`Missing CSS rule: ${selector}`);
  return css.slice(start, css.indexOf("}", start));
}

function theme(selector: string): Record<string, string> {
  return Object.fromEntries(
    [
      ...declarations(tokenCSS, selector).matchAll(
        /(--[\w-]+):\s*(#[\da-f]{6});/gi,
      ),
    ].map((match) => [match[1], match[2]]),
  );
}

/** Resolve the actual colour binding, so a style change is checked too. */
function colour(
  tokens: Record<string, string>,
  css: string,
  selector: string,
  property: string,
) {
  const token = declarations(css, selector).match(
    new RegExp(`(?:^|[;\\n])\\s*${property}:\\s*[^;]*?var\\((--[\\w-]+)\\)`),
  )?.[1];
  if (!token || !tokens[token])
    throw new Error(`Unresolved ${selector} ${property}`);
  return tokens[token];
}

/** WCAG relative luminance, with sRGB channels linearised before weighting. */
function luminance(hex: string) {
  const channels = hex
    .slice(1)
    .match(/../g)!
    .map((channel) => {
      const value = parseInt(channel, 16) / 255;
      return value <= 0.04045
        ? value / 12.92
        : ((value + 0.055) / 1.055) ** 2.4;
    });
  return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722;
}

function contrast(a: string, b: string) {
  const [dark, light] = [luminance(a), luminance(b)].sort((x, y) => x - y);
  return (light + 0.05) / (dark + 0.05);
}

describe("task team contrast", () => {
  it("uses the standard contrast calculation", () => {
    expect(contrast("#000000", "#ffffff")).toBe(21);
    expect(contrast("#ffffff", "#ffffff")).toBe(1);
    expect(contrast("#777777", "#ffffff")).toBeCloseTo(4.478, 3);
  });

  it.each([
    ["light", ":root"],
    ["system dark", ':root:not([data-theme="light"])'],
    ["chosen dark", ':root[data-theme="dark"]'],
  ])(
    "keeps text, pending states and the connector visible in %s",
    (_, selector) => {
      const tokens = theme(selector);
      const get = (css: string, rule: string, property = "color") =>
        colour(tokens, css, rule, property);
      const surface = get(requestCSS, ".task-stage-flow", "background");
      const pending = get(requestCSS, ".task-member-pending", "background");
      const text = get(requestCSS, ".task-stage-row");
      const soft = get(baseCSS, ".soft");
      const muted = get(baseCSS, ".muted");
      for (const background of [surface, pending]) {
        for (const foreground of [text, soft, muted]) {
          expect(
            contrast(foreground, background),
            `${foreground} on ${background}`,
          ).toBeGreaterThanOrEqual(4.5);
        }
      }
      // Clock and Check are decorative beside the explicit progress text,
      // but keep the usual 3:1 non-text contrast for their muted strokes.
      const marker = get(requestCSS, ".task-stage-marker");
      expect(contrast(marker, surface)).toBeGreaterThanOrEqual(3);
      expect(contrast(marker, pending)).toBeGreaterThanOrEqual(3);
      for (const tone of ["wait", "work", "done", "needs", "block"]) {
        expect(
          contrast(
            get(baseCSS, `.tone-${tone}`),
            get(baseCSS, `.tone-${tone}`, "background"),
          ),
          `${tone} pill`,
        ).toBeGreaterThanOrEqual(4.5);
      }
      // The quiet connector conveys no state: ordered rows, markers and text
      // do that. Preserve the existing theme's subtle line contrast (1.2:1),
      // rather than treating this decorative rule as text or a state icon.
      const connector = get(requestCSS, ".task-stage-flow > li", "background");
      expect(
        contrast(connector, surface),
        "decorative connector",
      ).toBeGreaterThanOrEqual(1.2);
    },
  );
});

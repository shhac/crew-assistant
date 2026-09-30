// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen, act } from "@testing-library/react";
import { ProviderIcon, providerGlyph } from "./ProviderIcon";
import { rememberChoices } from "./engines";
import { testChoices } from "./testEngines";
afterEach(cleanup);
describe("provider icons", () => {
  it.each(testChoices)(
    "labels $engine with its tooltip and accessible name",
    ({ engine, label }) => {
      rememberChoices(testChoices);
      render(<ProviderIcon engine={engine} />);
      const icon = screen.getByRole("img", { name: label });
      expect(icon.title).toBe(label);
      expect(icon.querySelector("svg")?.getAttribute("width")).toBe("14");
      expect(icon.querySelector("svg")?.getAttribute("stroke")).toBe(
        "currentColor",
      );
    },
  );
  // Design 1 is the target: retain the designer's exact supplied artwork.
  it.each([
    [
      "claude",
      "M17.8 6.3A7.5 7.5 0 1 0 17.8 17.7M18.5 8.2l2-1.2M19.1 12h2.4M18.5 15.8l2 1.2",
    ],
    [
      "codex",
      "M12 3.5l4 2.3v4.6l4 2.3v4.6l-4 2.3-4-2.3-4 2.3-4-2.3v-4.6l4-2.3V5.8zM8 5.8l4 2.3 4-2.3M4 12.7l4 2.3v4.6M20 12.7l-4 2.3v4.6",
    ],
    ["grok", "M5 18.5L18.5 5M6 6h7l-7 7zM18 11v7h-7"],
    [
      "openai-compatible",
      "M9 3.5v5M15 3.5v5M6.5 8.5h11v2.3a5.5 5.5 0 0 1-11 0zM12 16.3v4.2",
    ],
  ])(
    "renders Misha's %s mark with the specified stroke treatment",
    (engine, path) => {
      const { container } = render(<ProviderIcon engine={engine} />);
      const svg = container.querySelector("svg")!;
      expect(svg.querySelector("path")?.getAttribute("d")).toBe(path);
      expect(svg.getAttribute("viewBox")).toBe("0 0 24 24");
      expect(svg.getAttribute("fill")).toBe("none");
      expect(svg.getAttribute("stroke-width")).toBe("1.8");
      expect(svg.getAttribute("stroke-linecap")).toBe("round");
      expect(svg.getAttribute("stroke-linejoin")).toBe("round");
      expect(svg.getAttribute("aria-hidden")).toBe("true");
    },
  );
  it("uses the neutral glyph for API providers and unknown engines", () => {
    expect(providerGlyph("mistral")).toBe(providerGlyph("openai-compatible"));
    render(<ProviderIcon engine="mistral" />);
    expect(screen.getByRole("img", { name: "Mistral" }).title).toBe("Mistral");
  });
  it("uses a configured provider label", () => {
    render(<ProviderIcon engine="openai-compatible" label="OpenRouter" />);
    expect(screen.getByRole("img", { name: "OpenRouter" }).title).toBe(
      "OpenRouter",
    );
  });
  it("updates a fallback label when choices arrive", () => {
    render(<ProviderIcon engine="future" />);
    expect(screen.getByRole("img", { name: "Future" })).toBeTruthy();
    act(() =>
      rememberChoices([
        ...testChoices,
        { ...testChoices[0], engine: "future", label: "Future harness" },
      ]),
    );
    expect(screen.getByRole("img", { name: "Future harness" })).toBeTruthy();
  });
});

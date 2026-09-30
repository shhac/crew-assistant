import { describe, expect, it } from "vitest";
import css from "./styles/chat.css?raw";

describe("composer sizing", () => {
  it("grows from two lines to ten lines or 40vh, then scrolls at every pane width", () => {
    const rule = css.match(/\.composer textarea\s*\{([^}]+)\}/)![1];
    expect(rule).toContain("field-sizing: content");
    expect(rule).toContain("min-height: calc(2lh + 8px)");
    expect(rule).toContain("max-height: min(calc(10lh + 8px), 40vh)");
    expect(rule).toContain("overflow-y: auto");
    const overrides = [...css.matchAll(/([^{}]+)\{([^}]+)\}/g)].filter(
      ([, selector]) =>
        selector.includes("textarea") && selector.includes("chat-expanded"),
    );
    expect(overrides).toHaveLength(0);
  });
});

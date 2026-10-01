// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { createElement } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import { Composer } from "./Composer";
afterEach(cleanup);
import css from "./styles/chat.css?raw";

describe("composer sizing", () => {
  it("grows from two lines to ten lines or 40vh, then scrolls at every pane width", () => {
    for (const textOnly of [false, true]) {
      const view = render(
        createElement(Composer, {
          textOnly,
          value: "A long\nmessage",
          onChange: () => {},
          onSubmit: () => {},
          onExpand: () => {},
          name: "PM",
        }),
      );
      const field = screen.getByRole("textbox");
      expect(field.closest("form")?.className).toBe("composer");
      expect(field).toHaveProperty("rows", 2);
      if (textOnly) {
        expect(screen.queryByText(/drop text files/)).toBeNull();
        expect(screen.queryByRole("list", { name: "Attachments" })).toBeNull();
        expect(screen.getByText(/new line/)).toBeTruthy();
      }
      view.unmount();
    }
    const rule = css.match(/\.composer textarea\s*\{([^}]+)\}/)![1];
    expect(rule).toContain("field-sizing: content");
    expect(rule).toContain("min-height: calc(2lh + 8px)");
    expect(rule).toContain("max-height: min(calc(10lh + 8px), 40vh)");
    expect(rule).toContain("overflow-y: auto");
    const overrides = [...css.matchAll(/([^{}]+)\{([^}]+)\}/g)].filter(
      ([, selector]) =>
        selector.includes("textarea") &&
        (selector.includes("chat-expanded") || selector.includes("pm-chat")),
    );
    expect(overrides).toHaveLength(0);
  });
});

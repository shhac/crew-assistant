import { describe, expect, it } from "vitest";
import { href, parseRoute, requestHref } from "./router";

describe("addresses", () => {
  it("reads every page, and sends anything else to the inbox", () => {
    expect(parseRoute("")).toEqual({ page: "inbox" });
    expect(parseRoute("#/overview")).toEqual({ page: "inbox" });
    expect(parseRoute("#/decisions")).toEqual({ page: "inbox" });
    expect(parseRoute("#/projects")).toEqual({ page: "projects" });
    expect(parseRoute("#/memory")).toEqual({ page: "memory" });
    expect(parseRoute("#/settings/model")).toEqual({
      page: "settings",
      section: "model",
    });
    expect(parseRoute("#/projects/p%201")).toEqual({
      page: "project",
      id: "p 1",
      tab: "board",
    });
    expect(parseRoute("#/projects/p1/landing")).toEqual({
      page: "project",
      id: "p1",
      tab: "landing",
    });
    expect(parseRoute("#/projects/p1/nonsense")).toEqual({
      page: "project",
      id: "p1",
      tab: "board",
    });
    expect(parseRoute("#/projects/p1/requests/t%2F1")).toEqual({
      page: "project",
      id: "p1",
      tab: "board",
      request: "t/1",
    });
  });
  it("writes addresses that read back the same", () => {
    for (const hash of [
      "#/inbox",
      "#/projects",
      "#/memory",
      "#/settings",
      "#/settings/chat",
      "#/projects/p1",
      "#/projects/p1/team",
    ])
      expect(href(parseRoute(hash))).toBe(hash);
    expect(parseRoute(requestHref("p 1", "t/1"))).toEqual({
      page: "project",
      id: "p 1",
      tab: "board",
      request: "t/1",
    });
  });
});

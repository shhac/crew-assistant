import { describe, expect, it } from "vitest";
import { href, parseRoute, requestHref, seatHref } from "./router";

describe("addresses", () => {
  it("reads every page, and sends anything else to the inbox", () => {
    expect(parseRoute("")).toEqual({ page: "inbox" });
    expect(parseRoute("#/overview")).toEqual({ page: "inbox" });
    expect(parseRoute("#/decisions")).toEqual({ page: "inbox" });
    expect(parseRoute("#/projects")).toEqual({ page: "projects" });
    expect(parseRoute("#/memory")).toEqual({ page: "memory" });
    expect(parseRoute("#/team")).toEqual({ page: "team" });
    expect(parseRoute("#/team/m%201")).toEqual({ page: "member", id: "m 1" });
    expect(parseRoute("#/team/assistant/a%201")).toEqual({
      page: "assistant",
      id: "a 1",
    });
    expect(parseRoute("#/team/assistant")).toEqual({ page: "team" });
    expect(href({ page: "assistant", id: "a 1" })).toBe(
      "#/team/assistant/a%201",
    );
    expect(parseRoute("#/settings/model")).toEqual({
      page: "settings",
      section: "model",
    });
    expect(parseRoute("#/projects/p%201")).toEqual({
      page: "project",
      id: "p 1",
      tab: "board",
    });
    expect(parseRoute("#/projects/p1/config")).toEqual({
      page: "project",
      id: "p1",
      tab: "config",
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
      "#/inbox/decision%20%2F1",
      "#/projects",
      "#/memory",
      "#/team",
      "#/team/m1",
      "#/settings",
      "#/settings/chat",
      "#/projects/p1",
      "#/projects/p1/team",
      "#/projects/x/pm",
    ])
      expect(href(parseRoute(hash))).toBe(hash);
    expect(parseRoute(requestHref("p 1", "t/1"))).toEqual({
      page: "project",
      id: "p 1",
      tab: "board",
      request: "t/1",
    });
  });
  it("keeps a team member's panel open in the address", () => {
    expect(seatHref("p1", "t1", "Ada Lovelace")).toBe(
      "#/projects/p1/requests/t1/team/Ada%20Lovelace",
    );
    expect(parseRoute(seatHref("p1", "t1", "Ada Lovelace"))).toEqual({
      page: "project",
      id: "p1",
      tab: "board",
      request: "t1",
      seat: "Ada Lovelace",
    });
    expect(parseRoute("#/projects/p1/requests/t1/team")).toEqual({
      page: "project",
      id: "p1",
      tab: "board",
      request: "t1",
    });
  });
});

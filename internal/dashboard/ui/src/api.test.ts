import { afterEach, describe, expect, it, vi } from "vitest";
import { avatarURL, normalizeState, revisionFiles, setTeam } from "./api";

afterEach(() => vi.unstubAllGlobals());

function stubFetch(body: unknown) {
  const fetch = vi.fn(async (_path: string, _options?: RequestInit) => ({
    ok: true,
    status: 200,
    json: async () => body,
  }));
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

describe("project API", () => {
  it("defaults tasks when the daemon sends none", () => {
    expect(normalizeState({}).tasks).toEqual([]);
  });

  it("reads a draft's files, treating a missing list as empty", async () => {
    const fetch = stubFetch({ files: null });
    expect(await revisionFiles("p 1", "t/1", 3)).toEqual([]);
    expect(fetch.mock.calls[0][0]).toBe(
      "/api/projects/p%201/tasks/t%2F1/revisions/3",
    );
  });

  it("sends team choices as the strings the daemon expects", async () => {
    const fetch = stubFetch({});
    await setTeam("p1", {
      template: "draft",
      writer_engine: "codex",
      reviewer_engine: "claude",
      max_rounds: "4",
      deliver_to: "/out",
    });
    const [path, options] = fetch.mock.calls[0];
    expect(path).toBe("/api/projects/p1/team");
    expect(options?.method).toBe("PUT");
    expect(JSON.parse(options?.body as string)).toEqual({
      template: "draft",
      writer_engine: "codex",
      reviewer_engine: "claude",
      max_rounds: "4",
      deliver_to: "/out",
    });
  });
});

describe("drawn avatars", () => {
  const drawn = { image: "0123456789abcdef0123456789abcdef" };
  it("picks the stored size that stays sharp at twice the pixels shown", () => {
    const at = (px: number) => avatarURL(drawn, px);
    expect(at(16)).toBe(`/api/avatars/${drawn.image}/small`);
    expect(at(24)).toBe(`/api/avatars/${drawn.image}/small`);
    expect(at(25)).toBe(`/api/avatars/${drawn.image}/medium`);
    expect(at(64)).toBe(`/api/avatars/${drawn.image}/medium`);
    expect(at(65)).toBe(`/api/avatars/${drawn.image}/large`);
    expect(at(96)).toBe(`/api/avatars/${drawn.image}/large`);
  });

  it("has no address without a picture", () => {
    expect(avatarURL(undefined, 20)).toBeUndefined();
    expect(avatarURL({ shape: "orb" }, 20)).toBeUndefined();
  });
});

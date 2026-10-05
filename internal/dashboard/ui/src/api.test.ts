import { afterEach, describe, expect, it, vi } from "vitest";
import {
  normalizeState,
  revisionFiles,
  setTeam,
  taskTeamTurns,
  memberTeamTurns,
} from "./api";
import type { Repository, Team } from "./api";

afterEach(() => vi.unstubAllGlobals());

it("encodes history scopes and exclusive cursors, forwards cancellation and preserves missing/zero counts", async () => {
  const body = {
    turns: [
      {
        terminal: {
          usage: { input: null, cache_read: 0, cache_write: null, output: 0 },
        },
      },
    ],
    aggregate: { cache_read_share: null },
  };
  const fetch = stubFetch(body);
  const signal = new AbortController().signal;
  expect(await taskTeamTurns("p /", "t/1", "attempt/&", signal)).toEqual(body);
  expect(fetch.mock.calls[0][0]).toBe(
    "/api/projects/p%20%2F/tasks/t%2F1/team-turns?limit=50&before=attempt%2F%26",
  );
  expect(fetch.mock.calls[0][1]?.signal).toBe(signal);
  await memberTeamTurns("m /", undefined, signal);
  expect(fetch.mock.calls[1][0]).toBe(
    "/api/members/m%20%2F/team-turns?limit=50",
  );
  expect(fetch.mock.calls[1][1]?.signal).toBe(signal);
});

it.each([400, 401, 404, 500])(
  "propagates history read errors (%s)",
  async (status) => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: false,
        status,
        json: async () => ({ error: "Read refused" }),
      })),
    );
    await expect(memberTeamTurns("m")).rejects.toMatchObject({ status });
  },
);

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

it("preserves upgrade journals and rollback pins, including null waiting lists", () => {
  const upgrade = {
    step: "draining" as const,
    from: "v1.0.0",
    to: "v1.1.0",
    since: "2026-10-04T10:00:00Z",
    waiting_on: null,
  };
  const rollback = {
    from: "v1.0.0",
    to: "v1.1.0",
    failure: "health check failed",
    clear: "crew-assistant upgrade clear-rollback",
  };
  expect(normalizeState({ upgrade, rollback })).toMatchObject({
    upgrade,
    rollback,
  });
  expect(normalizeState({}).upgrade).toBeUndefined();
  expect(normalizeState({}).rollback).toBeUndefined();
});

it("keeps the repositories and teams the daemon sends", () => {
  const repositories: Repository[] = [
    { id: "repo-1", name: "app", path: "/work/app" },
  ];
  const teams: Team[] = [
    { id: "team-1", name: "App", template: "code", roles: [], max_rounds: 3 },
  ];
  expect(normalizeState({ repositories, teams })).toMatchObject({
    repositories,
    teams,
  });
  expect(normalizeState({}).repositories).toEqual([]);
  expect(normalizeState({}).teams).toEqual([]);
});

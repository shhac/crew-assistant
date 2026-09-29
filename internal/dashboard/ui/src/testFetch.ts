import { vi } from "vitest";

export type FetchCall = { path: string; options?: RequestInit };

/** A fetch response whose JSON is body; 4xx and 5xx are not ok. */
export const reply = (body: unknown, status = 200) => ({
  ok: status < 400,
  status,
  json: async () => body,
});

export type Reply = ReturnType<typeof reply>;

/** Stubs the global fetch with respond, keeping every call made through it. */
export function recordFetch(
  respond: (
    path: string,
    options?: RequestInit,
  ) => Reply | Promise<Reply> = () => reply({}),
) {
  const calls: FetchCall[] = [];
  const fetch = vi.fn(async (path: string, options?: RequestInit) => {
    calls.push({ path, options });
    return respond(path, options);
  });
  vi.stubGlobal("fetch", fetch);
  return { calls, fetch };
}

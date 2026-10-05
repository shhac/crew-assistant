import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { TeamTurnHistory } from "./TeamTurnHistory";
import {
  normalizeState,
  type TeamTurn,
  type TeamTurnHistoryPage,
  type TeamTurnUsage,
} from "./api";
import { MemberPage } from "./MemberPage";
import { RequestPanel } from "./RequestPanel";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
const unknown: TeamTurnUsage = {
  known: false,
  cache_known: false,
  final: false,
  status: "unknown",
  input: null,
  output: null,
  cache_read: null,
  cache_write: null,
};
const zero: TeamTurnUsage = {
  known: true,
  cache_known: true,
  final: true,
  status: "final",
  input: 0,
  output: 0,
  cache_read: 0,
  cache_write: 0,
};
const state = normalizeState({});
function turn(id: string, overrides: Partial<TeamTurn> = {}): TeamTurn {
  return {
    id,
    project_id: "old-project",
    member_id: "member",
    member_name: `Recorded ${id}`,
    seat: "old-seat",
    role: "pm",
    engine: "codex",
    model: "",
    provider_default: true,
    admitted_at: "2026-10-04T10:00:00Z",
    opening: null,
    lifecycle: "admitted",
    held: false,
    terminal: null,
    ...overrides,
  };
}
function page(
  turns: TeamTurn[] = [],
  next_before?: string,
  aggregate: Partial<TeamTurnHistoryPage["aggregate"]> = {},
): TeamTurnHistoryPage {
  return {
    turns,
    next_before,
    aggregate: {
      terminal_turns: 12,
      measured_turns: 7,
      missing_input_turns: 4,
      missing_cache_turns: 3,
      partial_only_turns: 2,
      input: 1000,
      cache_read: 730,
      cache_read_share: 0.73,
      ...aggregate,
    },
  };
}
function fetcher() {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
const reply = (body: unknown) => ({ ok: true, json: async () => body });
function deferred() {
  let resolve!: (value: ReturnType<typeof reply>) => void;
  const promise = new Promise<ReturnType<typeof reply>>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}
const scope = { member: "member" };

it("distinguishes initial loading, read failure, retry and successful empty history", async () => {
  const fetch = fetcher();
  const first = deferred();
  fetch.mockReturnValueOnce(first.promise).mockResolvedValueOnce(reply(page()));
  render(<TeamTurnHistory scope={scope} state={state} />);
  expect(screen.getByText("Loading team turns…")).toBeTruthy();
  expect(screen.queryByText("No recorded team turns.")).toBeNull();
  await act(async () =>
    first.resolve({
      ok: false,
      status: 500,
      json: async () => ({ error: "History failed" }),
    } as never),
  );
  expect(await screen.findByText(/History failed/)).toBeTruthy();
  expect(screen.queryByText("No recorded team turns.")).toBeNull();
  fireEvent.click(screen.getByText("Retry history"));
  expect(await screen.findByText("No recorded team turns.")).toBeTruthy();
});

it("renders recorded identity, cross-project and project-only attribution, lifecycle and nullable counts", async () => {
  const fetch = fetcher();
  fetch.mockResolvedValue(
    reply(
      page([
        turn("a", {
          terminal: { outcome: "completed", usage: zero, observed: unknown },
          lifecycle: "terminal",
          held: true,
          opening: { at: "", resumed: true },
        }),
        turn("b", {
          project_id: "another-project",
          task_id: "old-task",
          model: "configured",
          provider_default: false,
          terminal: {
            outcome: "failed",
            usage: {
              ...unknown,
              cache_known: true,
              cache_read: 0,
              cache_write: 0,
              status: "partial",
            },
            observed: { ...zero, input: 9, final: false, status: "partial" },
          },
          lifecycle: "terminal",
        }),
        turn("c", { lifecycle: "accepted" }),
        turn("d", { lifecycle: "opened" }),
        turn("e", {
          lifecycle: "terminal",
          terminal: {
            outcome: "interrupted",
            usage: unknown,
            observed: unknown,
          },
        }),
      ]),
    ),
  );
  render(<TeamTurnHistory scope={scope} state={state} />);
  expect(
    await screen.findByText(/Recorded a · pm · completed · Resumed/),
  ).toBeTruthy();
  expect(screen.getAllByText(/Provider default/).length).toBeGreaterThan(0);
  expect(screen.getByText(/configured/)).toBeTruthy();
  expect(screen.getByText(/Cleanup held/)).toBeTruthy();
  expect(
    screen.getByText(/Recorded c · pm · accepted · Unknown opening/),
  ).toBeTruthy();
  expect(screen.getByText(/Recorded d · pm · opened/)).toBeTruthy();
  expect(screen.getByText(/Recorded e · pm · interrupted/)).toBeTruthy();
  expect(screen.getAllByText("Unknown").length).toBeGreaterThan(0);
  expect(screen.getAllByText("0").length).toBe(9);
  expect(screen.getByText("9")).toBeTruthy();
  expect(screen.getByText("Terminal tokens · Partial usage")).toBeTruthy();
  expect(screen.getByText("Partial observations")).toBeTruthy();
  expect(
    screen.getByRole("link", { name: "old-task" }).getAttribute("href"),
  ).toContain("another-project/requests/old-task");
  expect(screen.getAllByText(/Project-only turn/).length).toBe(4);
  expect(screen.getByText("Weighted cache-read share: 73%")).toBeTruthy();
  expect(screen.getByText("7 of 12 terminal turns measured")).toBeTruthy();
  expect(
    screen.getByText(
      /Missing input: 4 turns · Missing cache accounting: 3 turns · Partial only: 2 turns/,
    ),
  ).toBeTruthy();
});

it.each([
  ["no_saved_thread", "No saved thread"],
  ["engine_changed", "Engine changed"],
  ["model_changed", "Model changed"],
  ["owner_requested", "Owner requested a fresh start"],
  ["harness_incompatible", "Saved thread incompatible with the harness"],
  ["harness_unavailable", "Harness unavailable for resumption"],
  ["new_reason", "new_reason"],
  ["", "Unknown"],
])("renders fresh reason %s", async (reason, copy) => {
  fetcher().mockResolvedValue(
    reply(
      page([
        turn("a", {
          opening: { at: "", resumed: false, fresh_reason: reason },
        }),
      ]),
    ),
  );
  render(<TeamTurnHistory scope={scope} state={state} />);
  expect(await screen.findByText(`Fresh reason: ${copy}`)).toBeTruthy();
  expect(screen.getByText(/· Fresh$/)).toBeTruthy();
});

it.each([0, null])(
  "uses backend share %s without inventing a denominator",
  async (share) => {
    const body = page([turn("a")]);
    body.aggregate.cache_read_share = share;
    fetcher().mockResolvedValue(reply(body));
    render(<TeamTurnHistory scope={scope} state={state} />);
    expect(
      await screen.findByText(
        `Weighted cache-read share: ${share === null ? "Unavailable" : "0%"}`,
      ),
    ).toBeTruthy();
  },
);

it("serializes pagination and polling, refreshes through the loaded oldest identity and removes duplicates", async () => {
  const fetch = fetcher();
  const later = deferred();
  fetch
    .mockResolvedValueOnce(reply(page([turn("b"), turn("c")], "c")))
    .mockReturnValueOnce(later.promise)
    .mockResolvedValueOnce(
      reply(page([turn("a"), turn("b", { lifecycle: "accepted" })], "b")),
    )
    .mockResolvedValueOnce(
      reply(
        page(
          [turn("c"), turn("d", { lifecycle: "opened" }), turn("unseen")],
          "unseen",
          { cache_read_share: 0.91, measured_turns: 9 },
        ),
      ),
    );
  const { rerender } = render(<TeamTurnHistory scope={scope} state={state} />);
  fireEvent.click(await screen.findByText("Load more turns"));
  rerender(<TeamTurnHistory scope={scope} state={{ ...state }} />);
  expect(fetch).toHaveBeenCalledTimes(2);
  await act(async () =>
    later.resolve(reply(page([turn("c"), turn("d")], "d"))),
  );
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(4));
  expect(await screen.findByText(/Recorded d · pm · opened/)).toBeTruthy();
  expect(screen.getAllByText(/Recorded c · pm/)).toHaveLength(1);
  expect(screen.queryByText(/Recorded unseen/)).toBeNull();
  expect(fetch.mock.calls[3][0]).toContain("before=b");
  expect(screen.getByText("Weighted cache-read share: 91%")).toBeTruthy();
  expect(screen.getByText("9 of 12 terminal turns measured")).toBeTruthy();
  fetch.mockResolvedValueOnce(
    reply(
      page([turn("unseen"), turn("tail")], undefined, {
        cache_read_share: 0.62,
        measured_turns: 10,
      }),
    ),
  );
  fireEvent.click(screen.getByText("Load more turns"));
  await screen.findByText(/Recorded unseen · pm/);
  expect(fetch.mock.calls[4][0]).toContain("before=d");
  expect(screen.getByText(/Recorded tail · pm/)).toBeTruthy();
  expect(screen.getByText("Weighted cache-read share: 62%")).toBeTruthy();
  expect(screen.getByText("10 of 12 terminal turns measured")).toBeTruthy();
  expect(screen.queryByText("Load more turns")).toBeNull();
});

it("preserves rows, aggregate and cursor after a later refresh page fails and retries", async () => {
  const fetch = fetcher();
  const later = deferred();
  fetch
    .mockResolvedValueOnce(reply(page([turn("b")], "b")))
    .mockResolvedValueOnce(reply(page([turn("c")], "c")))
    .mockResolvedValueOnce(
      reply(
        page([turn("a")], "a", { cache_read_share: 0.15, measured_turns: 11 }),
      ),
    )
    .mockReturnValueOnce(later.promise)
    .mockResolvedValueOnce(
      reply(
        page(
          [turn("a"), turn("b"), turn("c", { lifecycle: "accepted" })],
          undefined,
          {
            cache_read_share: 0.84,
            measured_turns: 10,
            missing_input_turns: 1,
          },
        ),
      ),
    );
  const { rerender } = render(<TeamTurnHistory scope={scope} state={state} />);
  fireEvent.click(await screen.findByText("Load more turns"));
  await screen.findByText(/Recorded c · pm · admitted/);
  rerender(<TeamTurnHistory scope={scope} state={{ ...state }} />);
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(4));
  expect(screen.getByText("Weighted cache-read share: 73%")).toBeTruthy();
  expect(screen.getByText("7 of 12 terminal turns measured")).toBeTruthy();
  expect(screen.queryByText(/Recorded a/)).toBeNull();
  await act(async () =>
    later.resolve({
      ok: false,
      status: 500,
      json: async () => ({ error: "Later page failed" }),
    } as never),
  );
  await screen.findByText("Later page failed");
  expect(screen.queryByText(/Recorded a/)).toBeNull();
  expect(screen.getByText(/Recorded c · pm · admitted/)).toBeTruthy();
  expect(screen.getByText("Load more turns")).toBeTruthy();
  expect(screen.getByText("7 of 12 terminal turns measured")).toBeTruthy();
  expect(screen.getByText("Weighted cache-read share: 73%")).toBeTruthy();
  fireEvent.click(screen.getByText("Retry history"));
  await screen.findByText(/Recorded c · pm · accepted/);
  expect(screen.getByText("Weighted cache-read share: 84%")).toBeTruthy();
  expect(screen.getByText("10 of 12 terminal turns measured")).toBeTruthy();
  expect(screen.getByText(/Missing input: 1 turns/)).toBeTruthy();
  expect(screen.queryByText("Load more turns")).toBeNull();
});

it.each(["refresh", "pagination"])(
  "keeps failures visible while a queued poll retries %s",
  async (operation) => {
    const fetch = fetcher();
    const failing = deferred();
    const retrying = deferred();
    fetch
      .mockResolvedValueOnce(reply(page([turn("a")], "a")))
      .mockReturnValueOnce(failing.promise)
      .mockReturnValueOnce(retrying.promise);
    const { rerender } = render(
      <TeamTurnHistory scope={scope} state={state} />,
    );
    await screen.findByText(/Recorded a · pm/);
    if (operation === "pagination")
      fireEvent.click(screen.getByText("Load more turns"));
    else rerender(<TeamTurnHistory scope={scope} state={{ ...state }} />);
    rerender(<TeamTurnHistory scope={scope} state={{ ...state }} />);
    expect(fetch).toHaveBeenCalledTimes(2);
    await act(async () =>
      failing.resolve({
        ok: false,
        status: 500,
        json: async () => ({ error: "Queued read failed" }),
      } as never),
    );
    expect(fetch).toHaveBeenCalledTimes(3);
    expect(screen.getByRole("alert").textContent).toContain(
      "Queued read failed",
    );
    expect(screen.getByText(/Recorded a · pm/)).toBeTruthy();
    expect(screen.getByText("Weighted cache-read share: 73%")).toBeTruthy();
    expect(screen.getByText("7 of 12 terminal turns measured")).toBeTruthy();
    expect(screen.getByText("Load more turns")).toBeTruthy();
    await act(async () =>
      retrying.resolve(
        reply(
          page([turn("a", { lifecycle: "accepted" })], "a", {
            cache_read_share: 0.25,
            measured_turns: 8,
          }),
        ),
      ),
    );
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByText(/Recorded a · pm · accepted/)).toBeTruthy();
    expect(screen.getByText("Weighted cache-read share: 25%")).toBeTruthy();
    expect(screen.getByText("8 of 12 terminal turns measured")).toBeTruthy();
  },
);

it("keeps a failed pagination cursor for retry", async () => {
  const fetch = fetcher();
  fetch
    .mockResolvedValueOnce(
      reply(
        page([turn("a")], "a", { cache_read_share: 0.15, measured_turns: 11 }),
      ),
    )
    .mockRejectedValueOnce(new Error("Page failed"))
    .mockResolvedValueOnce(reply(page([turn("b")])));
  render(<TeamTurnHistory scope={scope} state={state} />);
  fireEvent.click(await screen.findByText("Load more turns"));
  await screen.findByText("Page failed");
  fireEvent.click(screen.getByText("Load more turns"));
  await screen.findByText(/Recorded b · pm/);
  expect(fetch.mock.calls[2][0]).toContain("before=a");
});

it("ignores late responses from abandoned scopes and unmounted views", async () => {
  const fetch = fetcher();
  const late = deferred();
  const unmounted = deferred();
  fetch
    .mockReturnValueOnce(late.promise)
    .mockResolvedValueOnce(reply(page([turn("new")])))
    .mockReturnValueOnce(unmounted.promise);
  const { rerender, unmount } = render(
    <TeamTurnHistory scope={scope} state={state} />,
  );
  rerender(<TeamTurnHistory scope={{ member: "other" }} state={state} />);
  await screen.findByText(/Recorded new · pm/);
  await act(async () => late.resolve(reply(page([turn("old")]))));
  expect(screen.queryByText(/Recorded old/)).toBeNull();
  expect(fetch.mock.calls[0][1].signal.aborted).toBe(true);
  rerender(<TeamTurnHistory scope={{ member: "third" }} state={state} />);
  unmount();
  await act(async () => unmounted.resolve(reply(page([turn("abandoned")]))));
  expect(fetch.mock.calls[2][1].signal.aborted).toBe(true);
});

it("mounts history on member and task pages without filtering recorded assignments", async () => {
  const fetch = fetcher();
  fetch.mockResolvedValue(reply(page([turn("a")])));
  const member = {
    id: "member",
    name: "Current name",
    engine: "codex",
    kinds: [],
    learnings: [],
  };
  const project = {
    id: "p",
    title: "Project",
    status: "active",
    playbook: { roles: [] },
  };
  const task = {
    id: "t",
    project_id: "p",
    objective: "Task",
    status: "queued",
    criteria: [],
    messages: [],
  };
  const data = normalizeState({
    members: [member],
    projects: [project],
    tasks: [task],
  } as never);
  const mounted = render(
    <MemberPage
      member={data.members[0]}
      state={data}
      refresh={async () => {}}
    />,
  );
  await screen.findByText(/Recorded a · pm/);
  expect(fetch.mock.calls[0][0]).toBe(
    "/api/members/member/team-turns?limit=50",
  );
  mounted.unmount();
  render(
    <RequestPanel
      project={data.projects[0]}
      task={data.tasks[0]}
      state={data}
      refresh={async () => {}}
      onClose={() => {}}
      onSeat={() => {}}
    />,
  );
  await screen.findByText(/Recorded a · pm/);
  expect(
    fetch.mock.calls.some(
      ([url]) => url === "/api/projects/p/tasks/t/team-turns?limit=50",
    ),
  ).toBe(true);
});
// @vitest-environment jsdom

it("shows the session's unavailable tools and their reasons", async () => {
  fetcher().mockResolvedValue(
    reply(
      page([
        turn("api", {
          opening: {
            at: "2026-10-04T10:00:00Z",
            resumed: false,
            unavailable_tools: ["read_file", "search_files", "edit_file"].map(
              (name) => ({ name, reason: "file tools are off" }),
            ),
          },
        }),
      ]),
    ),
  );
  render(<TeamTurnHistory scope={scope} state={state} />);
  expect(
    await screen.findByText(
      "Unavailable tools: read_file: file tools are off; search_files: file tools are off; edit_file: file tools are off",
    ),
  ).toBeTruthy();
});

it("omits unavailable tools for older opening records", async () => {
  fetcher().mockResolvedValue(
    reply(
      page([
        turn("old", { opening: { at: "2026-10-04T10:00:00Z", resumed: true } }),
      ]),
    ),
  );
  render(<TeamTurnHistory scope={scope} state={state} />);
  await screen.findAllByText(/Recorded old/);
  expect(screen.queryByText(/Unavailable tools:/)).toBeNull();
});

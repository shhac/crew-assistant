import { useEffect, useRef, useState } from "react";
import {
  errorText,
  memberTeamTurns,
  taskTeamTurns,
  type TeamTurnHistoryPage,
} from "./api";

/** Serialize reads and publish complete refreshes, keeping the last good reading. */
export function useTeamTurnHistory(
  scope: { member: string } | { project: string; task: string },
  poll: unknown,
) {
  const key = JSON.stringify(scope);
  const [view, setView] = useState<{
    key: string;
    page?: TeamTurnHistoryPage;
    busy: boolean;
    error: string;
  }>({ key, busy: true, error: "" });
  const actions = useRef<(more: boolean) => void>(() => {});
  useEffect(() => {
    const abort = new AbortController();
    let page: TeamTurnHistoryPage | undefined;
    let busy = false;
    let pending = false;
    let error = "";
    const read = (before?: string) =>
      "member" in scope
        ? memberTeamTurns(scope.member, before, abort.signal)
        : taskTeamTurns(scope.project, scope.task, before, abort.signal);
    const publish = () => {
      if (!abort.signal.aborted) setView({ key, page, busy, error });
    };
    async function run(more: boolean) {
      if (busy) {
        if (!more) pending = true;
        return;
      }
      if (more && !page?.next_before) return;
      busy = true;
      publish();
      try {
        const oldest = page?.turns.at(-1)?.id;
        let next = await read(more ? page?.next_before : undefined);
        if (abort.signal.aborted) return;
        const turns = more ? [...page!.turns, ...next.turns] : [...next.turns];
        const visited = new Set<string>();
        while (
          !more &&
          oldest &&
          !turns.some((t) => t.id === oldest) &&
          next.next_before
        ) {
          if (visited.has(next.next_before))
            throw new Error("History cursor did not advance");
          visited.add(next.next_before);
          next = await read(next.next_before);
          if (abort.signal.aborted) return;
          turns.push(...next.turns);
        }
        const unique = new Map(turns.map((t) => [t.id, t]));
        let merged = [...unique.values()];
        let cursor = next.next_before;
        if (!more && oldest) {
          const end = merged.findIndex((t) => t.id === oldest);
          if (end >= 0 && end < merged.length - 1) {
            merged = merged.slice(0, end + 1);
            cursor = oldest;
          }
        }
        if (!abort.signal.aborted)
          page = {
            turns: merged,
            aggregate: next.aggregate,
            next_before: cursor,
          };
        error = "";
        busy = false;
        publish();
      } catch (e) {
        busy = false;
        error = errorText(e);
        publish();
      }
      if (pending && !abort.signal.aborted) {
        pending = false;
        void run(false);
      }
    }
    actions.current = (more) => {
      void run(more);
    };
    void run(false);
    return () => abort.abort();
  }, [key]);
  const previous = useRef(poll);
  useEffect(() => {
    if (previous.current !== poll) {
      previous.current = poll;
      actions.current(false);
    }
  }, [poll]);
  return {
    ...(view.key === key ? view : { busy: true, error: "", page: undefined }),
    retry: () => actions.current(false),
    more: () => actions.current(true),
  };
}

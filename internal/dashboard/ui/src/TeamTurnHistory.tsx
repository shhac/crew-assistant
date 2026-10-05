import { Fragment } from "react";
import { type State, type TeamTurnUsage } from "./api";
import { engineLabel } from "./engines";
import { memberHref, projectHref, requestHref } from "./router";
import { ErrorNotice } from "./ui";
import { useTeamTurnHistory } from "./useTeamTurnHistory";

export const freshReasons: Record<string, string> = {
  no_saved_thread: "No saved thread",
  engine_changed: "Engine changed",
  model_changed: "Model changed",
  owner_requested: "Owner requested a fresh start",
  harness_incompatible: "Saved thread incompatible with the harness",
  harness_unavailable: "Harness unavailable for resumption",
};

function Usage({ usage, label }: { usage?: TeamTurnUsage; label: string }) {
  return (
    <div>
      <p className="label">
        {label}
        {usage?.status === "partial" && label !== "Partial observations"
          ? " · Partial usage"
          : ""}
      </p>
      <dl className="facts">
        {(
          [
            ["Input", "input"],
            ["Cache-read", "cache_read"],
            ["Cache-write", "cache_write"],
            ["Output", "output"],
          ] as const
        ).map(([name, key]) => (
          <Fragment key={key}>
            <dt>{name}</dt>
            <dd>{usage?.[key] ?? "Unknown"}</dd>
          </Fragment>
        ))}
      </dl>
    </div>
  );
}

export function TeamTurnHistory({
  scope,
  state,
}: {
  scope: { member: string } | { project: string; task: string };
  state: State;
}) {
  const { page, busy, error, retry, more } = useTeamTurnHistory(scope, state);
  const aggregate = page?.aggregate;
  return (
    <section className="section" aria-label="Team turns">
      <div className="section-title">
        <h3>Team turns</h3>
      </div>
      <ErrorNotice error={error} />
      {error && (
        <button className="btn btn-sm" disabled={busy} onClick={retry}>
          Retry history
        </button>
      )}
      {busy && (
        <p className="muted" role="status">
          {page ? "Refreshing team turns…" : "Loading team turns…"}
        </p>
      )}
      {aggregate && (
        <div className="card draft-detail disclosure-body">
          <p>
            Weighted cache-read share:{" "}
            {aggregate.cache_read_share === null
              ? "Unavailable"
              : `${Number((aggregate.cache_read_share * 100).toFixed(1))}%`}
          </p>
          <p>
            {aggregate.measured_turns} of {aggregate.terminal_turns} terminal
            turns measured
          </p>
          <p className="small soft">
            Missing input: {aggregate.missing_input_turns} turns · Missing cache
            accounting: {aggregate.missing_cache_turns} turns · Partial only:{" "}
            {aggregate.partial_only_turns} turns. Missing categories can
            overlap. Coverage spans the entire history.
          </p>
        </div>
      )}
      {page && !page.turns.length && (
        <p className="muted">No recorded team turns.</p>
      )}
      {!!page?.turns.length && (
        <ul className="card rows">
          {page.turns.map((turn) => (
            <li key={turn.id}>
              <details className="draft">
                <summary>
                  {turn.member_name || turn.member_id || turn.seat} ·{" "}
                  {turn.role} · {turn.terminal?.outcome ?? turn.lifecycle} ·{" "}
                  {turn.opening
                    ? turn.opening.resumed
                      ? "Resumed"
                      : "Fresh"
                    : "Unknown opening"}
                </summary>
                <div className="draft-detail">
                  <p>
                    <time dateTime={turn.admitted_at}>{turn.admitted_at}</time>{" "}
                    · {engineLabel(turn.engine)} ·{" "}
                    {turn.provider_default ? "Provider default" : turn.model}
                  </p>
                  <p>
                    Seat: {turn.seat} · Lifecycle: {turn.lifecycle}
                    {turn.terminal && ` · Outcome: ${turn.terminal.outcome}`}
                    {turn.held && " · Cleanup held"}
                  </p>
                  {turn.opening && !turn.opening.resumed && (
                    <p>
                      Fresh reason:{" "}
                      {turn.opening.fresh_reason
                        ? (freshReasons[turn.opening.fresh_reason] ??
                          turn.opening.fresh_reason)
                        : "Unknown"}
                    </p>
                  )}
                  <p>
                    <a href={projectHref(turn.project_id)}>
                      {state.projects.find((p) => p.id === turn.project_id)
                        ?.title ?? turn.project_id}
                    </a>
                    {turn.task_id ? (
                      <>
                        {" "}
                        ·{" "}
                        <a href={requestHref(turn.project_id, turn.task_id)}>
                          {state.tasks.find((t) => t.id === turn.task_id)
                            ?.objective ?? turn.task_id}
                        </a>
                      </>
                    ) : (
                      " · Project-only turn"
                    )}
                    {turn.member_id && (
                      <>
                        {" "}
                        ·{" "}
                        <a href={memberHref(turn.member_id)}>
                          {turn.member_name || turn.member_id}
                        </a>
                      </>
                    )}
                  </p>
                  {!!turn.opening?.unavailable_tools?.length && (
                    <p>
                      Unavailable tools:{" "}
                      {turn.opening.unavailable_tools
                        .map((tool) => `${tool.name}: ${tool.reason}`)
                        .join("; ")}
                    </p>
                  )}
                  <Usage usage={turn.terminal?.usage} label="Terminal tokens" />
                  {turn.terminal?.observed.status !== "unknown" &&
                    turn.terminal && (
                      <Usage
                        usage={turn.terminal.observed}
                        label="Partial observations"
                      />
                    )}
                </div>
              </details>
            </li>
          ))}
        </ul>
      )}
      {page?.next_before && (
        <button className="btn btn-sm" disabled={busy} onClick={more}>
          Load more turns
        </button>
      )}
    </section>
  );
}

import { useEffect, useState } from "react";
import { getUsage, type EngineUsage } from "./api";
import { engineLabel } from "./members";
import { recordedTime } from "./ui";

/**
 * How often usage is looked at: reading a login spends no usage, but an
 * engine that has none left only needs checking now and then.
 */
export const usageRefreshMs = 5 * 60_000;

/** A reset today as a time; a later one with its day. */
export function resetLabel(value?: string) {
  const at = recordedTime(value);
  if (!at) return "";
  const soon = at.valueOf() - Date.now() < 24 * 60 * 60_000;
  return at.toLocaleString(
    undefined,
    soon
      ? { hour: "2-digit", minute: "2-digit" }
      : { weekday: "short", hour: "2-digit", minute: "2-digit" },
  );
}

const percent = (n: number) => `${Math.round(n)}%`;

/** What an engine's row says, only from what its CLI reported. */
function describe(u: EngineUsage) {
  const name = engineLabel(u.engine);
  // Read first: the small models skip an engine resting for its rate limit
  // whether or not a figure could be read since.
  const limited = resetLabel(u.rate_limited_until);
  if (u.missing || u.windows.length === 0)
    return {
      name,
      // Out when last measured stays out until a check measures otherwise.
      tone: u.level === "exhausted" || limited ? "tone-block" : "",
      summary: [
        limited && `rate-limited until ${limited}`,
        u.missing || "usage not reported",
      ]
        .filter(Boolean)
        .join(", "),
    };
  const tightest = u.windows.reduce((a, b) =>
    b.left_percent < a.left_percent ? b : a,
  );
  const resets = resetLabel(
    u.level === "low" || u.level === "exhausted"
      ? u.resets_at
      : tightest.resets_at,
  );
  const parts = [
    limited && `rate-limited until ${limited}`,
    u.level === "exhausted"
      ? "out of usage"
      : `${u.level === "low" ? "low, " : ""}${percent(tightest.left_percent)} left`,
    resets && `resets ${resets}`,
    u.using_overage && "using extra usage",
  ].filter(Boolean);
  return {
    name,
    tone:
      u.level === "exhausted" || limited
        ? "tone-block"
        : u.level === "low"
          ? "tone-needs"
          : "",
    summary: parts.join(", "),
    left: tightest.left_percent,
    shown:
      u.level === "exhausted"
        ? "Out of usage"
        : limited
          ? "Rate-limited"
          : `${percent(tightest.left_percent)} left`,
    resets,
    windows:
      u.windows.length > 1
        ? u.windows
            .map((w) => `${w.name} ${percent(w.left_percent)}`)
            .join(" · ")
        : "",
  };
}

/**
 * Usage each engine's login has left, under the Running line. It keeps its
 * own slow refresh apart from the dashboard's, and holds nothing that takes
 * focus, so a look never disturbs the chat or an open request, and a failed
 * one only says so here.
 */
export function UsageStatus() {
  const [usage, setUsage] = useState<EngineUsage[] | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let live = true;
    const look = () =>
      getUsage().then(
        (value) => {
          if (!live) return;
          const ok = Array.isArray(value);
          if (ok) setUsage(value);
          setFailed(!ok);
        },
        () => live && setFailed(true),
      );
    void look();
    const timer = setInterval(() => {
      if (!document.hidden) void look();
    }, usageRefreshMs);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, []);
  if (!usage)
    return failed ? <p className="hint">Usage couldn't be checked.</p> : null;
  return (
    <div className="usage">
      <ul className="usage-list" aria-label="Usage left">
        {usage.map((u) => {
          const row = describe(u);
          return (
            <li
              key={u.engine}
              className={`usage-row ${row.tone}`.trim()}
              aria-label={`${row.name}: ${row.summary}`}
            >
              <p className="usage-line">
                <span className="usage-name">{row.name}</span>
                <span className="usage-figure">{row.shown ?? row.summary}</span>
              </p>
              {row.left !== undefined && (
                <div
                  className="usage-bar"
                  role="meter"
                  aria-label={`${row.name} usage left`}
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={Math.round(row.left)}
                >
                  <span style={{ width: `${row.left}%` }} />
                </div>
              )}
              {(row.resets || row.windows) && (
                <p className="hint">
                  {[row.resets && `Resets ${row.resets}`, row.windows]
                    .filter(Boolean)
                    .join(" · ")}
                </p>
              )}
            </li>
          );
        })}
      </ul>
      {failed && <p className="hint">Couldn't check usage just now.</p>}
    </div>
  );
}

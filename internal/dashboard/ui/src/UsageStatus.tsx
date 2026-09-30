import { useEffect, useState } from "react";
import { getUsage, type EngineCredits, type EngineUsage } from "./api";
import { engineLabel } from "./engines";
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

export function asOfLabel(value?: string) {
  const at = recordedTime(value);
  if (!at) return "";
  return `as of ${at.toLocaleString(
    undefined,
    Date.now() - at.valueOf() < 24 * 60 * 60_000
      ? { hour: "2-digit", minute: "2-digit" }
      : { weekday: "short", hour: "2-digit", minute: "2-digit" },
  )}`;
}

const percent = (n: number) => `${Math.round(n)}%`;

/** "Credits: 12.50 USD", or "12.5 credits" when the unit is credits itself. */
export function creditsLabel(credits?: EngineCredits) {
  if (!credits?.balance) return "";
  if (credits.unit === "credits") return `${credits.balance} credits`;
  return `Credits: ${credits.balance} ${credits.unit}`.trim();
}

/** What an engine's row says, only from what its CLI reported. */
function describe(u: EngineUsage) {
  const name = u.label || engineLabel(u.engine);
  const credits = creditsLabel(u.credits);
  // Read first: the small models skip an engine resting for its rate limit
  // whether or not a figure could be read since.
  const limited = resetLabel(u.rate_limited_until);
  // An API provider reports no usage; it is listed only while it rests.
  if (u.provider)
    return {
      name,
      tone: "tone-block",
      summary: `rate-limited until ${limited}`,
      shown: `Rate-limited until ${limited}`,
      credits: "",
    };
  if (u.windows.length === 0 && !credits)
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
      credits,
    };
  if (u.windows.length === 0)
    return {
      name,
      tone: u.level === "exhausted" || limited ? "tone-block" : "",
      summary: [
        limited && `rate-limited until ${limited}`,
        u.level === "exhausted" && "out of usage",
        u.missing,
        credits,
      ]
        .filter(Boolean)
        .join(", "),
      shown: u.level === "exhausted" ? "Out of usage" : credits,
      credits: u.level === "exhausted" ? credits : "",
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
    credits,
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
    credits,
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
  const [readAt, setReadAt] = useState<string>();
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let live = true;
    const look = () =>
      getUsage().then(
        (value) => {
          if (!live) return;
          const ok = Array.isArray(value);
          if (ok) {
            setUsage(value);
            setReadAt(new Date().toISOString());
          }
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
          const hasFigures = u.windows.length > 0 || !!u.credits;
          const asOf = u.as_of || (failed && hasFigures ? readAt : undefined);
          // A past reset belonged to the earlier read, not a coming reset.
          const resetStillAhead = (value?: string) => {
            const at = recordedTime(value);
            return asOf && at && at.valueOf() <= Date.now() ? undefined : value;
          };
          const row = describe({
            ...u,
            resets_at: resetStillAhead(u.resets_at),
            windows: u.windows.map((w) => ({
              ...w,
              resets_at: resetStillAhead(w.resets_at),
            })),
          });
          const age = asOfLabel(asOf);
          const reason =
            asOf || (u.windows.length === 0 && hasFigures) ? u.missing : "";
          return (
            <li
              key={u.provider ? `${u.engine}/${u.provider}` : u.engine}
              className={`usage-row ${row.tone}`.trim()}
              aria-label={`${row.name}: ${[row.summary, age].filter(Boolean).join(", ")}`}
            >
              <p className="usage-line">
                <span className="usage-name">{row.name}</span>
                <span className="usage-figure">
                  {[row.shown ?? row.summary, age].filter(Boolean).join(" · ")}
                </span>
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
              {(row.resets || row.windows || row.credits || reason) && (
                <p className="hint">
                  {[
                    row.resets && `Resets ${row.resets}`,
                    row.windows,
                    row.credits,
                    reason,
                  ]
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

import { useState, type ReactNode } from "react";
import { criteriaLines, errorText } from "./api";

const icons: Record<string, string> = {
  Inbox:
    "M22 12h-6l-2 3h-4l-2-3H2M5.5 5.1L2 12v6a2 2 0 002 2h16a2 2 0 002-2v-6l-3.5-6.9A2 2 0 0016.8 4H7.2a2 2 0 00-1.7 1.1z",
  Projects:
    "M3 7a2 2 0 012-2h4l2 2h8a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2z",
  Memory: "M19 21l-7-5-7 5V5a2 2 0 012-2h10a2 2 0 012 2z",
  Settings:
    "M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6",
  Search: "M11 4a7 7 0 100 14 7 7 0 000-14zM20 20l-3.5-3.5",
  Eye: "M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12zM12 9a3 3 0 100 6 3 3 0 000-6z",
  Branch:
    "M6 3.5a2.5 2.5 0 100 5 2.5 2.5 0 000-5zM6 15.5a2.5 2.5 0 100 5 2.5 2.5 0 000-5zM18 5.5a2.5 2.5 0 100 5 2.5 2.5 0 000-5zM6 8.5v7M18 10.5c0 4-5 4-9.5 6",
  Copy: "M9 9h12v12H9zM5 15V5a2 2 0 012-2h10",
  Up: "M12 19V5M6 11l6-6 6 6",
  Down: "M12 5v14M6 13l6 6 6-6",
  ChevronDown: "M6 9l6 6 6-6",
  Arrow: "M5 12h14M13 6l6 6-6 6",
  Plus: "M12 5v14M5 12h14",
  Close: "M6 6l12 12M18 6L6 18",
  Send: "M22 2L11 13M22 2l-7 20-4-9-9-4z",
  Check: "M5 12l4 4L19 6",
  Pause: "M8 5v14M16 5v14",
  Play: "M7 4l14 8-14 8z",
  Message: "M21 12a8 8 0 01-11.6 7.1L4 20l1-4.6A8 8 0 1121 12z",
  Lock: "M6 10h12v11H6zM8 10V6a4 4 0 018 0v4",
  Chevron: "M9 5l7 7-7 7",
  Expand:
    "M8 3H3v5M16 3h5v5M3 16v5h5M21 16v5h-5M3 3l6 6M21 3l-6 6M3 21l6-6M21 21l-6-6",
  Shrink: "M3 8h5V3M21 8h-5V3M8 21v-5H3M16 21v-5h5",
  Alert: "M12 3l9 17H3zM12 9v5M12 17h.01",
  Clock: "M12 3a9 9 0 100 18 9 9 0 000-18zM12 7v5l3 2",
};

export function Icon({ name, size = 16 }: { name: string; size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={icons[name] || icons.Projects} />
    </svg>
  );
}

export function ErrorNotice({ error }: { error: string }) {
  return error ? (
    <div className="error" role="alert">
      {error}
    </div>
  ) : null;
}

/** A status in its colour, always in the wording given (never re-cased). */
export function Pill({
  tone = "",
  dot = false,
  children,
}: {
  tone?: string;
  dot?: boolean;
  children: ReactNode;
}) {
  return (
    <span className={`pill${tone ? ` tone-${tone}` : ""}`}>
      {dot && <span className="dot" />}
      {children}
    </span>
  );
}

export function humanStatus(value: string) {
  return (value || "pending").replaceAll("_", " ");
}

/**
 * A time the daemon actually recorded. Go serializes an unset time as year
 * one, so that reaches the client as a real-looking timestamp; treating it as
 * a date prints "Jan 1, 12:00 AM" for "never happened".
 */
export function recordedTime(value?: string): Date | null {
  if (!value || value.startsWith("0001-")) return null;
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? null : date;
}

export function dateLabel(value?: string) {
  return (
    recordedTime(value)?.toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    }) || ""
  );
}

/** The longer form, for places with room for a full date. */
export function fullDateLabel(value?: string) {
  return (
    recordedTime(value)?.toLocaleString(undefined, {
      dateStyle: "medium",
      timeStyle: "short",
    }) || ""
  );
}

/** Elapsed time in the owner's terms; exact timestamps stay available alongside. */
export function sinceLabel(value?: string) {
  const then = recordedTime(value);
  if (!then) return "";
  // Recorded times can come from more than one clock, so a stamp slightly
  // ahead of this one is an expected input. Reporting it as
  // unusable would make work that just reported look abandoned.
  const minutes = Math.round((Date.now() - then.valueOf()) / 60000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} ${hours === 1 ? "hour" : "hours"} ago`;
  const days = Math.round(hours / 24);
  return `${days} ${days === 1 ? "day" : "days"} ago`;
}

/**
 * Acceptance criteria as recorded, or a plain statement that none were. The
 * marker is optional so the project brief can keep its dotted presentation
 * without a second copy of the list.
 */
export function CriteriaList({
  criteria,
  empty = "No criteria recorded.",
  marker = false,
}: {
  criteria: Parameters<typeof criteriaLines>[0];
  empty?: string;
  marker?: boolean;
}) {
  const lines = criteriaLines(criteria);
  if (!lines.length) return <p className="muted">{empty}</p>;
  return (
    <ul className={marker ? "criteria-list" : undefined}>
      {lines.map((line, i) => (
        <li key={i}>
          {marker && <span className="criteria-dot" />}
          {line}
        </li>
      ))}
    </ul>
  );
}

/**
 * useAction runs one owner action at a time: it marks the component busy,
 * clears the last error, and shows a failure as the error.
 */
export function useAction() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function run(action: () => Promise<unknown>) {
    setBusy(true);
    setError("");
    try {
      await action();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }
  return { busy, error, setError, run };
}

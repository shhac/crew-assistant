import type { ReactNode } from "react";
import { criteriaLines } from "./api";

const icons: Record<string, string> = {
  Overview: "M3 3h7v7H3zM14 3h7v7h-7zM3 14h7v7H3zM14 14h7v7h-7z",
  Projects: "M3 7h7l2-3h9v16H3z",
  Decisions: "M12 3l9 9-9 9-9-9zM12 8v5M12 16h.01",
  Memory: "M6 3h12v18l-6-4-6 4z",
  Settings: "M4 7h16M4 17h16M8 4v6M16 14v6",
  Arrow: "M5 12h14M13 6l6 6-6 6",
  Plus: "M12 5v14M5 12h14",
  Close: "M6 6l12 12M18 6L6 18",
  Send: "M12 19V5M6 11l6-6 6 6",
  Check: "M5 12l4 4L19 6",
  Pause: "M8 5v14M16 5v14",
  Play: "M7 4l14 8-14 8z",
  Message: "M4 4h16v13H9l-5 4z",
  Lock: "M6 10h12v11H6zM8 10V6a4 4 0 018 0v4",
  Chevron: "M9 5l7 7-7 7",
  Expand:
    "M8 3H3v5M16 3h5v5M3 16v5h5M21 16v5h-5M3 3l6 6M21 3l-6 6M3 21l6-6M21 21l-6-6",
  Shrink: "M3 8h5V3M21 8h-5V3M8 21v-5H3M16 21v-5h5",
  Alert: "M12 3l9 17H3zM12 9v5M12 17h.01",
  Clock: "M12 3a9 9 0 100 18 9 9 0 000-18zM12 7v5l3 2",
};

export function Icon({ name, size = 18 }: { name: string; size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={icons[name] || icons.Projects} />
    </svg>
  );
}

export function Mark({ small = false }: { small?: boolean }) {
  return (
    <span
      className={`assistant-mark ${small ? "small" : ""}`}
      aria-hidden="true"
    >
      <i />
      <i />
      <i />
      <i />
    </span>
  );
}

export function Status({
  children,
  tone = "",
  plain = false,
}: {
  children: ReactNode;
  tone?: string;
  /** Keep a label's own wording instead of capitalising every word. */
  plain?: boolean;
}) {
  return (
    <span className={`status ${tone}${plain ? " plain" : ""}`}>
      <span className="status-dot" />
      {children}
    </span>
  );
}

export function ErrorNotice({ error }: { error: string }) {
  return error ? (
    <div className="error-notice" role="alert">
      {error}
    </div>
  ) : null;
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

export function Empty({
  icon,
  title,
  children,
  action,
}: {
  icon: string;
  title: string;
  children: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="empty-state">
      <span className="empty-icon">
        <Icon name={icon} size={24} />
      </span>
      <h3>{title}</h3>
      <p>{children}</p>
      {action}
    </div>
  );
}

export function PageHeading({
  eyebrow,
  title,
  description,
  action,
}: {
  eyebrow: string;
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="page-heading">
      <div>
        <p className="eyebrow">{eyebrow}</p>
        <h1>{title}</h1>
        <p className="page-description">{description}</p>
      </div>
      {action}
    </div>
  );
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

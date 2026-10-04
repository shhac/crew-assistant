import type { LinearRef } from "./api";

export interface Option {
  id: string;
  name: string;
}
export function LinearSources({
  links,
  compact = false,
}: {
  links?: LinearRef[];
  compact?: boolean;
}) {
  if (!links?.length) return null;
  return (
    <div className={compact ? "board-card-meta muted small" : "card"}>
      {!compact && <h3>Source issue</h3>}
      {links.map((l) => (
        <p key={`${l.connection_id}.${l.profile}.${l.id}`}>
          <LinearSourceLink link={l} compact={compact} />
        </p>
      ))}
    </div>
  );
}
export function LinearSourceLink({
  link,
  compact = false,
}: {
  link: LinearRef;
  compact?: boolean;
}) {
  return (
    <a
      className={compact ? "board-card-source-link" : undefined}
      href={link.url}
      target="_blank"
      rel="noopener noreferrer"
    >
      {link.identifier}
      {!compact && `: ${link.title}`}
    </a>
  );
}

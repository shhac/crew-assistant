import type { ReactNode } from "react";

/**
 * One card of settings under its heading. Given an `id`, the heading names
 * the card by reference, for cards whose heading is linked to elsewhere.
 */
export function Panel({
  title,
  id,
  children,
}: {
  title: string;
  id?: string;
  children: ReactNode;
}) {
  return (
    <section
      className="tab-panel card settings-panel"
      aria-label={id ? undefined : title}
      aria-labelledby={id}
    >
      <h2 id={id}>{title}</h2>
      {children}
    </section>
  );
}

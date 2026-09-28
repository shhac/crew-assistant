import { AttachmentList } from "./RequestAttachments";
import type { Evidence, Task, Verdict } from "./api";

/** What each kind of finding in words is about. */
const evidenceLabel: Record<string, string> = {
  page: "Page",
  console: "Console",
  network: "Network",
  screenshot: "Screenshots",
};

/**
 * What QA saw using the app, under its verdict: the screenshots it took,
 * kept with the request, and its page, console and network findings.
 */
export function VerdictEvidence({
  task,
  verdict,
}: {
  task: Task;
  verdict: Verdict;
}) {
  const evidence = verdict.evidence ?? [];
  if (!evidence.length) return null;
  const kept = new Set(
    evidence.flatMap((e) => (e.attachment ? [e.attachment] : [])),
  );
  const shots = (task.attachments ?? []).filter((a) => kept.has(a.id));
  const words = evidence.filter((e): e is Evidence & { text: string } =>
    Boolean(!e.attachment && e.text),
  );
  return (
    <div className="verdict-evidence" aria-label={`What ${verdict.role} saw`}>
      <AttachmentList task={task} attachments={shots} />
      {!!words.length && (
        <ul className="findings">
          {words.map((e, i) => (
            <li key={i}>
              <span className="muted">{evidenceLabel[e.kind] ?? e.kind}: </span>
              {e.text}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

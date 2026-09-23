import { ProjectLink } from "./ProjectLink";
import { dateLabel, humanStatus, Icon, Status } from "./ui";
import type { Decision, Project } from "./api";

/**
 * Closed decisions, kept inspectable. A dismissal closed a question without
 * answering it, so it is presented distinctly from a recorded answer rather
 * than as a quieter variant of one.
 */
export function DecisionHistory({
  decisions,
  projects,
}: {
  decisions: Decision[];
  projects: Project[];
}) {
  if (!decisions.length) return null;
  return (
    <section className="section-block decision-history">
      <div className="section-heading">
        <h2>Decision history</h2>
        <span className="section-note">
          {decisions.length} closed {decisions.length === 1 ? "question" : "questions"}
        </span>
      </div>
      {decisions.map((d) => {
        const dismissed = d.status === "dismissed";
        const project = projects.find((p) => p.id === d.project_id);
        const resolved = dateLabel(d.resolved_at);
        return (
          <div
            className={`history-row ${dismissed ? "dismissed" : "answered"}`}
            key={d.id}
          >
            <span>
              <Icon name={dismissed ? "Close" : "Check"} size={16} />
              {d.title}
            </span>
            <div>
              <Status tone={dismissed ? "" : "green"}>
                {humanStatus(d.status)}
              </Status>
              {(d.answer || d.resolution_reason) && (
                <p className="muted">
                  {dismissed ? "Reason: " : "Answer: "}
                  {d.resolution_reason || d.answer}
                </p>
              )}
              <p className="history-meta">
                {project && <ProjectLink project={project} />}
                {project && resolved ? " · " : ""}
                {resolved && (
                  <>
                    Closed{" "}
                    <time dateTime={d.resolved_at}>{resolved}</time>
                  </>
                )}
              </p>
              {(d.context || d.recommendation) && (
                <details className="history-context">
                  <summary>Original context</summary>
                  {d.context && <p>{d.context}</p>}
                  {d.recommendation && (
                    <p className="muted">
                      Recommended at the time: {d.recommendation}
                    </p>
                  )}
                  {dismissed && (
                    <p className="field-hint">
                      Dismissal closed this question. It did not approve or
                      restart any work.
                    </p>
                  )}
                </details>
              )}
            </div>
          </div>
        );
      })}
    </section>
  );
}

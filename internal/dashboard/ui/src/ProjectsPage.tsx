import { useState } from "react";
import { projectHref } from "./router";
import {
  decisionFor,
  finished,
  landsBy,
  leadRequest,
  needsYou,
  projectGroup,
  projectGroups,
  projectKind,
  projectTasks,
  requestStep,
} from "./stages";
import { Pill, sinceLabel } from "./ui";
import type { Project, State } from "./api";

export function ProjectsPage({
  state,
  onNew,
}: {
  state: State;
  onNew: () => void;
}) {
  const [query, setQuery] = useState("");
  const matches = (p: Project) =>
    p.title.toLowerCase().includes(query.trim().toLowerCase());
  const open = state.projects.filter(
    (p) => p.status !== "completed" && matches(p),
  );
  const done = state.projects.filter(
    (p) => p.status === "completed" && matches(p),
  );
  return (
    <div className="page">
      <header className="page-header page-header-actions">
        <h1>Projects</h1>
        <div className="actions">
          {state.projects.length > 4 && (
            <label>
              <span className="sr-only">Find a project</span>
              <input
                className="field"
                type="search"
                value={query}
                placeholder="Find a project"
                onChange={(e) => setQuery(e.target.value)}
              />
            </label>
          )}
          <button className="btn btn-primary" onClick={onNew}>
            New project
          </button>
        </div>
      </header>
      {!state.projects.length ? (
        <div className="empty card">
          <p>No projects yet.</p>
        </div>
      ) : (
        <div className="project-table card">
          <div className="project-head label" aria-hidden="true">
            <span>Project</span>
            <span>Now</span>
            <span>Lands by</span>
            <span>Updated</span>
          </div>
          {projectGroups.map(({ group, label }) => {
            const members = open.filter(
              (p) => projectGroup(projectTasks(p, state.tasks)) === group,
            );
            if (!members.length) return null;
            return (
              <section key={group} aria-label={label} className="project-group">
                <h2 className="project-group-title">
                  {label} <span className="count">{members.length}</span>
                </h2>
                <ul className="rows">
                  {members.map((p) => (
                    <ProjectRow key={p.id} project={p} state={state} />
                  ))}
                </ul>
              </section>
            );
          })}
          {done.length > 0 && (
            <details className="disclosure project-group">
              <summary className="project-group-title">
                Finished <span className="count">{done.length}</span>
              </summary>
              <ul className="rows">
                {done.map((p) => (
                  <ProjectRow key={p.id} project={p} state={state} />
                ))}
              </ul>
            </details>
          )}
        </div>
      )}
    </div>
  );
}

function ProjectRow({ project, state }: { project: Project; state: State }) {
  const own = projectTasks(project, state.tasks);
  const lead = leadRequest(own);
  const decision = lead ? decisionFor(lead, state.decisions) : undefined;
  const open = own.filter((t) => !finished(t)).length;
  const kind = projectKind(project.playbook);
  return (
    <li>
      <a className="project-row" href={projectHref(project.id)}>
        <span className="project-name">
          <span className="project-title">{project.title}</span>
          <span className="muted small">
            {kind}
            {open ? ` · ${open} open` : ""}
          </span>
        </span>
        <span className="project-now">
          {lead ? (
            <>
              {needsYou(lead) && (
                <Pill tone="needs">{requestStep(lead, decision)}</Pill>
              )}
              <span>
                {needsYou(lead)
                  ? lead.objective
                  : `${lead.objective}: ${requestStep(lead, decision)}`}
              </span>
            </>
          ) : (
            <span className="muted">
              {project.playbook ? "Nothing asked for" : "No team yet"}
            </span>
          )}
        </span>
        <span className="soft small">{landsBy(project.playbook)}</span>
        <span className="muted small">{sinceLabel(project.updated_at)}</span>
      </a>
    </li>
  );
}

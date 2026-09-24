import { useState } from "react";
import { Avatar } from "./Avatar";
import { MemberForm } from "./MemberForm";
import { DrawingStatus, LookForm } from "./Redraw";
import { href, projectHref } from "./router";
import { Learnings } from "./MemberLearnings";
import { memberProjects, memberSummary } from "./members";
import { ErrorNotice, useAction } from "./ui";
import { deleteMember, redrawMember, type Member, type State } from "./api";

export function MemberPage({
  member,
  state,
  refresh,
}: {
  member: Member;
  state: State;
  refresh: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const [redrawing, setRedrawing] = useState(false);
  const projects = memberProjects(member, state.projects);
  return (
    <div className="page team">
      <header className="page-header">
        <nav className="crumbs" aria-label="Breadcrumb">
          <a href={href({ page: "team" })}>Team</a>
          <span aria-hidden="true">/</span>
          <span aria-current="page">{member.name}</span>
        </nav>
        {editing ? (
          <section className="card team-form">
            <MemberForm
              member={member}
              onCancel={() => setEditing(false)}
              onSaved={async () => {
                await refresh();
                setEditing(false);
              }}
            />
          </section>
        ) : (
          <div className="member-head">
            <Avatar of={member} size={64} />
            <div className="member-head-text">
              <h1>{member.name}</h1>
              <p className="soft">{memberSummary(member)}</p>
            </div>
            <div className="actions">
              {!member.drawing && !redrawing && (
                <button
                  type="button"
                  className="btn btn-quiet btn-sm"
                  onClick={() => setRedrawing(true)}
                >
                  Redraw
                </button>
              )}
              <button
                type="button"
                className="btn btn-sm"
                onClick={() => setEditing(true)}
              >
                Edit
              </button>
            </div>
          </div>
        )}
        {!editing &&
          (redrawing && !member.drawing ? (
            <section className="card team-form">
              <LookForm
                id="member-look"
                face={member}
                onCancel={() => setRedrawing(false)}
                onRedraw={async (look) => {
                  await redrawMember(member.id, look);
                  await refresh();
                  setRedrawing(false);
                }}
              />
            </section>
          ) : (
            <DrawingStatus face={member} />
          ))}
      </header>
      {projects.length > 0 && (
        <section className="section" aria-label="Projects">
          <div className="section-title">
            <h2>Projects</h2>
          </div>
          <ul className="card rows">
            {projects.map((p) => (
              <li key={p.id}>
                <a className="member-project" href={projectHref(p.id, "team")}>
                  {p.title}
                </a>
              </li>
            ))}
          </ul>
        </section>
      )}
      <Learnings member={member} state={state} refresh={refresh} />
      <DeleteMember member={member} refresh={refresh} />
    </div>
  );
}

function DeleteMember({
  member,
  refresh,
}: {
  member: Member;
  refresh: () => Promise<void>;
}) {
  const [confirming, setConfirming] = useState(false);
  const { busy, error, run } = useAction();
  async function remove() {
    await run(async () => {
      await deleteMember(member.id);
      window.location.hash = href({ page: "team" });
      await refresh();
    });
  }
  return (
    <section className="section member-delete" aria-label="Delete member">
      {confirming ? (
        <div className="actions">
          <button
            type="button"
            className="btn btn-danger btn-sm"
            disabled={busy}
            onClick={() => void remove()}
          >
            Delete {member.name}
          </button>
          <button
            type="button"
            className="btn btn-quiet btn-sm"
            disabled={busy}
            onClick={() => setConfirming(false)}
          >
            Keep
          </button>
          <span className="muted small">Project teams keep their copy.</span>
        </div>
      ) : (
        <div className="actions">
          <button
            type="button"
            className="btn btn-quiet btn-sm"
            onClick={() => setConfirming(true)}
          >
            Delete member
          </button>
        </div>
      )}
      <ErrorNotice error={error} />
    </section>
  );
}

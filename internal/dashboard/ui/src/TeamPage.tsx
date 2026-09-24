import { useState } from "react";
import { MemberForm } from "./MemberForm";
import { memberHref } from "./router";
import { memberProjects, memberSummary } from "./stages";
import { Avatar, counted } from "./ui";
import type { Member, State } from "./api";

export function TeamPage({
  state,
  refresh,
}: {
  state: State;
  refresh: () => Promise<void>;
}) {
  const [adding, setAdding] = useState(false);
  const empty = !state.members.length;
  const newMember = (
    <button className="btn btn-primary" onClick={() => setAdding(true)}>
      New member
    </button>
  );
  return (
    <div className="page team">
      <header className="page-header page-header-actions">
        <h1>Team</h1>
        {!empty && !adding && newMember}
      </header>
      {adding && (
        <section className="card team-form">
          <MemberForm
            onCancel={() => setAdding(false)}
            onSaved={async (member) => {
              await refresh();
              window.location.hash = memberHref(member.id);
            }}
          />
        </section>
      )}
      {empty && !adding && (
        <div className="empty card">
          <p>
            Members you set up here can join any project's team and keep what
            they learn.
          </p>
          {newMember}
        </div>
      )}
      {!empty && (
        <ul className="member-list">
          {state.members.map((m) => (
            <MemberCard key={m.id} member={m} state={state} />
          ))}
        </ul>
      )}
    </div>
  );
}

function MemberCard({ member, state }: { member: Member; state: State }) {
  const projects = memberProjects(member, state.projects).length;
  const learnings = member.learnings.length;
  const usage = [
    projects ? `In ${counted(projects, "project")}` : "",
    learnings ? counted(learnings, "learning") : "",
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <li>
      <a className="member-card card" href={memberHref(member.id)}>
        <Avatar of={member} size={40} />
        <span className="member-card-text">
          <span className="member-name">{member.name}</span>
          <span className="soft small">{memberSummary(member)}</span>
          {usage && <span className="muted small">{usage}</span>}
        </span>
      </a>
    </li>
  );
}

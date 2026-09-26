import { useState } from "react";
import { MemberForm } from "./MemberForm";
import { AssistantForm } from "./AssistantForm";
import {
  resetSuggestion,
  SuggestIdentity,
  type SetupSubject,
  type Suggestion,
} from "./SuggestIdentity";
import { assistantHref, href, memberHref } from "./router";
import { assistantSummary, memberProjects, memberSummary } from "./members";
import { Avatar } from "./Avatar";
import { counted } from "./ui";
import type { AssistantProfile, Member, State } from "./api";

export function TeamPage({
  state,
  refresh,
}: {
  state: State;
  refresh: () => Promise<void>;
}) {
  const [adding, setAdding] = useState<SetupSubject | null>(null);
  const [suggestion, setSuggestion] = useState<Suggestion>();
  const add = (subject: SetupSubject | null) => {
    setAdding(subject);
    setSuggestion(undefined);
  };
  // Who a suggestion was used for is added; the next one starts afresh.
  async function added(subject: SetupSubject, to: string) {
    if (suggestion) await resetSuggestion(subject).catch(() => {});
    await refresh();
    window.location.hash = to;
  }
  const suggest = (subject: SetupSubject) => (
    <SuggestIdentity
      subject={subject}
      askerName={state.assistant.name}
      demo={state.demo}
      onUse={setSuggestion}
    />
  );
  const seated = state.assistant.id;
  const noMembers = !state.members.length;
  const newAssistant = (
    <button className="btn btn-primary" onClick={() => add("assistant")}>
      New assistant
    </button>
  );
  const newMember = (
    <button className="btn btn-primary" onClick={() => add("member")}>
      New member
    </button>
  );
  return (
    <div className="page team">
      <header className="page-header">
        <h1>Team</h1>
      </header>
      <section className="section" aria-labelledby="team-assistants">
        <div className="section-title">
          <h2 id="team-assistants">Assistants</h2>
          {adding !== "assistant" && newAssistant}
        </div>
        {adding === "assistant" && (
          <>
            <section className="card team-form">
              <AssistantForm
                suggestion={suggestion}
                onCancel={() => add(null)}
                onSaved={(assistant) =>
                  added("assistant", assistantHref(assistant.id))
                }
              />
            </section>
            {suggest("assistant")}
          </>
        )}
        {state.assistants.length > 0 ? (
          <ul className="member-list">
            {state.assistants.map((a) => (
              <AssistantCard
                key={a.id}
                assistant={a}
                seated={a.id === seated}
              />
            ))}
          </ul>
        ) : (
          adding !== "assistant" && (
            <div className="empty card">
              <p>
                An assistant runs your projects with you. Set one up, then
                choose it in Settings.
              </p>
            </div>
          )
        )}
        {state.assistants.length > 0 && !seated && (
          <p className="muted small">
            No assistant is in the seat, so none answers.{" "}
            <a href={href({ page: "settings", section: "assistant" })}>
              Choose your assistant
            </a>
          </p>
        )}
      </section>
      <section className="section" aria-labelledby="team-members">
        <div className="section-title">
          <h2 id="team-members">Members</h2>
          {!noMembers && adding !== "member" && newMember}
        </div>
        {adding === "member" && (
          <>
            <section className="card team-form">
              <MemberForm
                suggestion={suggestion}
                onCancel={() => add(null)}
                onSaved={(member) => added("member", memberHref(member.id))}
              />
            </section>
            {suggest("member")}
          </>
        )}
        {noMembers && adding !== "member" && (
          <div className="empty card">
            <p>
              Members you set up here can join any project's team and keep what
              they learn.
            </p>
            {newMember}
          </div>
        )}
        {!noMembers && (
          <ul className="member-list">
            {state.members.map((m) => (
              <MemberCard key={m.id} member={m} state={state} />
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function AssistantCard({
  assistant,
  seated,
}: {
  assistant: AssistantProfile;
  seated: boolean;
}) {
  return (
    <li>
      <a className="member-card card" href={assistantHref(assistant.id)}>
        <Avatar of={assistant} size={40} />
        <span className="member-card-text">
          <span className="member-name">{assistant.name}</span>
          <span className="soft small">{assistantSummary(assistant)}</span>
          {assistant.drawing ? (
            <span className="muted small">Drawing…</span>
          ) : (
            seated && <span className="muted small">Your assistant</span>
          )}
        </span>
      </a>
    </li>
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
          {member.drawing ? (
            <span className="muted small">Drawing…</span>
          ) : (
            usage && <span className="muted small">{usage}</span>
          )}
        </span>
      </a>
    </li>
  );
}

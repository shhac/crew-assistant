import { shownProvider, useProviders } from "./providers";
import { ProviderIcon } from "./ProviderIcon";
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
import {
  assistantDetail,
  holds,
  kindWord,
  memberKinds,
  memberProjects,
  memberDetail,
} from "./members";
import { engineLabel } from "./engines";
import { Avatar } from "./Avatar";
import { counted } from "./ui";
import type { AssistantProfile, Member, MemberKind, State } from "./api";

type RoleShown = MemberKind | "all";

const roleChoices: { id: RoleShown; label: string }[] = [
  { id: "all", label: "All roles" },
  ...memberKinds,
];

const memberSorts = [
  { id: "name", label: "Name (A–Z)" },
  { id: "role", label: "Role" },
  { id: "engine", label: "Engine" },
  { id: "newest", label: "Newest first" },
] as const;
type MemberSort = (typeof memberSorts)[number]["id"];

const roleShownOf = (value: string): RoleShown =>
  memberKinds.find((k) => k.id === value)?.id ?? "all";
const memberSortOf = (value: string): MemberSort =>
  memberSorts.find((s) => s.id === value)?.id ?? "name";

const byName = (a: Member, b: Member) =>
  a.name.localeCompare(b.name, undefined, { sensitivity: "base" });

// A member with several kinds sorts with the earliest of them in team order.
const roleRank = (m: Member) =>
  Math.min(
    ...m.kinds.map((kind) => memberKinds.findIndex((k) => k.id === kind)),
    memberKinds.length,
  );

const createdAt = (m: Member) => {
  const time = Date.parse(m.created_at ?? "");
  return Number.isNaN(time) ? -Infinity : time;
};

const memberOrders: Record<MemberSort, (a: Member, b: Member) => number> = {
  name: byName,
  role: (a, b) => roleRank(a) - roleRank(b) || byName(a, b),
  engine: (a, b) =>
    engineLabel(a.engine).localeCompare(engineLabel(b.engine)) || byName(a, b),
  newest: (a, b) => createdAt(b) - createdAt(a) || byName(a, b),
};

function storedChoice(key: string) {
  try {
    return localStorage.getItem(key) ?? "";
  } catch {
    return "";
  }
}

/** A choice kept in this browser, so the page opens the way it was left. */
function useRemembered<T extends string>(
  key: string,
  read: (value: string) => T,
) {
  const [value, setValue] = useState(() => read(storedChoice(key)));
  const remember = (next: T) => {
    setValue(next);
    try {
      localStorage.setItem(key, next);
    } catch {
      // Storage can be unavailable; the choice still holds until a reload.
    }
  };
  return [value, remember] as const;
}

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
  const [shown, setShown] = useRemembered(
    "crew-assistant.team-role",
    roleShownOf,
  );
  const [sort, setSort] = useRemembered(
    "crew-assistant.team-sort",
    memberSortOf,
  );
  const providers = useProviders(
    state.assistants.some((a) => a.model.engine === "openai-compatible"),
  );
  const seated = state.assistant.id;
  const noMembers = !state.members.length;
  const members = state.members
    .filter((m) => shown === "all" || holds(m, shown))
    .sort(memberOrders[sort]);
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
                providerLabel={
                  a.model.engine === "openai-compatible"
                    ? (providers.find(
                        (p) => p.id === shownProvider(a.model.provider),
                      )?.label ?? "Another API")
                    : undefined
                }
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
          <div className="member-controls">
            <div
              className="segmented role-filter"
              role="group"
              aria-label="Show"
            >
              {roleChoices.map((choice) => (
                <button
                  key={choice.id}
                  type="button"
                  aria-pressed={shown === choice.id}
                  onClick={() => setShown(choice.id)}
                >
                  {choice.label}
                </button>
              ))}
            </div>
            <label className="member-sort" htmlFor="team-sort">
              Sort by
              <select
                id="team-sort"
                className="field"
                value={sort}
                onChange={(e) => setSort(memberSortOf(e.target.value))}
              >
                {memberSorts.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.label}
                  </option>
                ))}
              </select>
            </label>
          </div>
        )}
        {!noMembers && members.length > 0 && (
          <ul className="member-list">
            {members.map((m) => (
              <MemberCard key={m.id} member={m} state={state} />
            ))}
          </ul>
        )}
        {!noMembers && !members.length && (
          <p className="muted small">
            No member holds the {kindWord(shown)} role yet.
          </p>
        )}
      </section>
    </div>
  );
}

function AssistantCard({
  assistant,
  seated,
  providerLabel,
}: {
  assistant: AssistantProfile;
  seated: boolean;
  providerLabel?: string;
}) {
  return (
    <li>
      <a className="member-card card" href={assistantHref(assistant.id)}>
        <Avatar of={assistant} size={40} />
        <span className="member-card-text">
          <span className="member-name">
            {assistant.name}
            <ProviderIcon
              engine={assistant.model.engine}
              label={providerLabel}
            />
          </span>
          {assistantDetail(assistant) && (
            <span className="soft small">{assistantDetail(assistant)}</span>
          )}
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
          <span className="member-name">
            {member.name}
            <ProviderIcon engine={member.engine} />
          </span>
          {memberDetail(member) && (
            <span className="soft small">{memberDetail(member)}</span>
          )}
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

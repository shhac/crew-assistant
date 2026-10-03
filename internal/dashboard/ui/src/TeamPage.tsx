import { shownProvider, useProviders } from "./providers";
import { ProviderIcon } from "./ProviderIcon";
import { useState } from "react";
import { useRemembered } from "./remembered";
import { byTitle } from "./stages";
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
  { id: "most-projects", label: "Most projects" },
  { id: "fewest-projects", label: "Fewest projects" },
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

// projects counts the open projects a member is on, for the sorts that
// find who is free to assign and who may be over-assigned.
const memberOrders = (
  projects: (m: Member) => number,
): Record<MemberSort, (a: Member, b: Member) => number> => ({
  name: byName,
  role: (a, b) => roleRank(a) - roleRank(b) || byName(a, b),
  engine: (a, b) =>
    engineLabel(a.engine).localeCompare(engineLabel(b.engine)) || byName(a, b),
  newest: (a, b) => createdAt(b) - createdAt(a) || byName(a, b),
  "most-projects": (a, b) => projects(b) - projects(a) || byName(a, b),
  "fewest-projects": (a, b) => projects(a) - projects(b) || byName(a, b),
});

type ProjectMatch = "any" | "all";

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
  const [picked, setPicked] = useState<string[]>([]);
  const [match, setMatch] = useState<ProjectMatch>("any");
  const openProjects = state.projects
    .filter((p) => p.status !== "completed")
    .sort(byTitle);
  const projectsOf = (m: Member) =>
    new Set(memberProjects(m, state.projects).map((p) => p.id));
  const onPicked = (m: Member) => {
    if (!picked.length) return true;
    const on = projectsOf(m);
    return match === "all"
      ? picked.every((id) => on.has(id))
      : picked.some((id) => on.has(id));
  };
  const toggle = (id: string) =>
    setPicked((now) =>
      now.includes(id) ? now.filter((p) => p !== id) : [...now, id],
    );
  const members = state.members
    .filter((m) => (shown === "all" || holds(m, shown)) && onPicked(m))
    .sort(memberOrders((m) => projectsOf(m).size)[sort]);
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
            <label className="sort-picker" htmlFor="team-sort">
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
            {openProjects.length > 0 && (
              <details className="project-filter">
                <summary className="field">
                  {picked.length
                    ? `On ${match} of ${counted(picked.length, "project")}`
                    : "On any project"}
                </summary>
                <div className="project-filter-menu card">
                  <div
                    className="segmented"
                    role="group"
                    aria-label="Match projects"
                  >
                    {(["any", "all"] as const).map((m) => (
                      <button
                        key={m}
                        type="button"
                        aria-pressed={match === m}
                        onClick={() => setMatch(m)}
                      >
                        {m === "any" ? "Any" : "All"}
                      </button>
                    ))}
                  </div>
                  <fieldset>
                    <legend className="sr-only">Projects</legend>
                    {openProjects.map((p) => (
                      <label key={p.id} className="check">
                        <input
                          type="checkbox"
                          checked={picked.includes(p.id)}
                          onChange={() => toggle(p.id)}
                        />
                        <span>{p.title}</span>
                      </label>
                    ))}
                  </fieldset>
                  {picked.length > 0 && (
                    <button
                      type="button"
                      className="btn btn-quiet"
                      onClick={() => setPicked([])}
                    >
                      Clear
                    </button>
                  )}
                </div>
              </details>
            )}
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
            {picked.length
              ? `No member${shown === "all" ? "" : ` holding the ${kindWord(shown)} role`} is on ${match} of the chosen projects.`
              : `No member holds the ${kindWord(shown)} role yet.`}
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

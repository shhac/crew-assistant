import { useState } from "react";
import { Avatar } from "./Avatar";
import { AssistantForm } from "./AssistantForm";
import { DrawingStatus, LookForm } from "./Redraw";
import { href } from "./router";
import { assistantSummary } from "./members";
import { ConfirmAction, ErrorNotice, recordedTime, useAction } from "./ui";
import {
  deleteAssistant,
  redrawAssistant,
  type AssistantProfile,
  type State,
} from "./api";

/** One of the owner's assistants, laid out as a member's page is. */
export function AssistantPage({
  profile,
  state,
  refresh,
}: {
  profile: AssistantProfile;
  state: State;
  refresh: () => Promise<void>;
}) {
  const seated = state.assistant.id === profile.id;
  // Corrected memories are kept on the Memory page, not here.
  const memories = state.memories.filter(
    (m) => m.assistant === profile.id && !recordedTime(m.superseded_at),
  );
  return (
    <div className="page team">
      <header className="page-header">
        <nav className="crumbs" aria-label="Breadcrumb">
          <a href={href({ page: "team" })}>Team</a>
          <span aria-hidden="true">/</span>
          <span aria-current="page">{profile.name}</span>
        </nav>
        <AssistantHead profile={profile} refresh={refresh} />
      </header>
      <section className="section" aria-label="Seat">
        <p className="muted small">
          {seated
            ? `${profile.name} is your assistant. `
            : `${profile.name} isn't in the seat. `}
          <a href={href({ page: "settings", section: "assistant" })}>
            Choose your assistant in Settings
          </a>
        </p>
      </section>
      <section className="section" aria-label="What it remembers about itself">
        <div className="section-title">
          <h2>What {profile.name} remembers about itself</h2>
        </div>
        {memories.length ? (
          <ul className="card rows">
            {memories.map((m) => (
              <li key={m.id} className="memory-row">
                <p>{m.content}</p>
              </li>
            ))}
          </ul>
        ) : (
          <p className="muted small">Nothing yet.</p>
        )}
        <p className="muted small">
          What it knows about you, every assistant shares.{" "}
          <a href={href({ page: "memory" })}>See everything remembered</a>
        </p>
      </section>
      <DeleteAssistant profile={profile} seated={seated} refresh={refresh} />
    </div>
  );
}

function AssistantHead({
  profile,
  refresh,
}: {
  profile: AssistantProfile;
  refresh: () => Promise<void>;
}) {
  const [mode, setMode] = useState<"view" | "edit" | "redraw">("view");
  if (mode === "edit")
    return (
      <section className="card team-form">
        <AssistantForm
          assistant={profile}
          onCancel={() => setMode("view")}
          onSaved={async () => {
            await refresh();
            setMode("view");
          }}
        />
      </section>
    );
  const head = (
    <div className="member-head">
      <Avatar of={profile} size={64} />
      <div className="member-head-text">
        <h1>{profile.name}</h1>
        <p className="soft">{assistantSummary(profile)}</p>
        {profile.personality && <p>{profile.personality}</p>}
      </div>
      <div className="actions">
        {mode === "view" && !profile.drawing && (
          <button
            type="button"
            className="btn btn-quiet btn-sm"
            onClick={() => setMode("redraw")}
          >
            Redraw
          </button>
        )}
        <button
          type="button"
          className="btn btn-sm"
          onClick={() => setMode("edit")}
        >
          Edit
        </button>
      </div>
    </div>
  );
  // A drawing that started meanwhile shows its status in place of the form.
  if (mode === "redraw" && !profile.drawing)
    return (
      <>
        {head}
        <section className="card team-form">
          <LookForm
            id="assistant-look"
            face={profile}
            onCancel={() => setMode("view")}
            onRedraw={async (look) => {
              await redrawAssistant(profile.id, look);
              await refresh();
              setMode("view");
            }}
          />
        </section>
      </>
    );
  return (
    <>
      {head}
      <DrawingStatus face={profile} />
    </>
  );
}

function DeleteAssistant({
  profile,
  seated,
  refresh,
}: {
  profile: AssistantProfile;
  seated: boolean;
  refresh: () => Promise<void>;
}) {
  const [confirming, setConfirming] = useState(false);
  const { busy, error, run } = useAction();
  async function remove() {
    await run(async () => {
      await deleteAssistant(profile.id);
      window.location.hash = href({ page: "team" });
      await refresh();
    });
  }
  return (
    <section className="section member-delete" aria-label="Delete assistant">
      <ConfirmAction
        confirming={confirming}
        onConfirming={setConfirming}
        trigger="Delete assistant"
        confirm={`Delete ${profile.name}`}
        busy={busy}
        onConfirm={() => void remove()}
        note={
          <>
            {seated &&
              "No assistant answers until you choose another in Settings. "}
            What {profile.name} remembers about itself goes too; what it knows
            about you stays.
          </>
        }
      />
      <ErrorNotice error={error} />
    </section>
  );
}

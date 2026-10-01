import type { Project, Role, State, Task } from "./api";
import {
  holds,
  roleAtWork,
  taskRoles,
  workingKind,
  workingSeats,
} from "./members";
import {
  prWords,
  decisionFor,
  decisionKind,
  finished,
  isCode,
  needsYou,
  isOpenMessage,
  taskPlaybook,
  verdictOutcome,
  waitingWords,
} from "./stages";
import { waitingLine, workingParts } from "./turns";
import { sinceLabel } from "./ui";

export interface FlowRow {
  key: string;
  kind: string;
  label: string;
  roundLabel?: string;
  role?: Role;
  state: "not-started" | "working" | "done" | "interrupted";
  support: string;
  exception?: { tone: "needs" | "block" | "stopped"; text: string };
  previous: { label: string; outcome: string; at?: string }[];
}

/** Derive progress from the pinned record; working wins over a just-written artifact. */
export function stageFlow(
  task: Task,
  project: Project,
  state: State,
): FlowRow[] {
  const team = taskRoles(task, project);
  const pinned = task.playbook?.roles ?? [];
  const stopped = task.status === "stopped";
  const queued = task.status === "queued" || task.status === "triage";
  const code = isCode(taskPlaybook(task, project));
  const build = code ? "Build" : "Write";
  const artifact = code ? "build" : "draft";
  const revisions = [...(task.revisions ?? [])].sort((a, b) => b.n - a.n);
  const latest = revisions[0];
  const n = Math.max(task.round, latest?.n ?? 1, 1);
  const designs = task.design ?? [];
  const design = designs.at(-1);
  const currentStage = stopped ? (task.place ?? task.stage) : task.stage;
  const askingKind =
    task.with_designer ||
    task.status === "designing" ||
    currentStage === "designing"
      ? design?.step === "researching"
        ? "researcher"
        : design?.step === "writing"
          ? "implementer"
          : ""
      : "";
  const research = task.research ?? [];
  const pass = research.at(-1);
  const verdicts = [...(task.verdicts ?? [])].reverse();
  const active = workingSeats({ ...task, roles: team }, state.turns);
  const currentKind =
    task.with_designer ||
    task.status === "designing" ||
    currentStage === "designing"
      ? "designer"
      : currentStage === "researching"
        ? "researcher"
        : currentStage === "implementing"
          ? "implementer"
          : currentStage === "qa"
            ? "qa"
            : currentStage === "reviewing"
              ? "reviewer"
              : task.status === "triage" || currentStage === "triage"
                ? "pm"
                : task.status === "writing"
                  ? "implementer"
                  : task.status === "researching"
                    ? "researcher"
                    : "";
  const lookup = (name: string, kind: string): Role =>
    team.find((r) => r.name === name) ??
    pinned.find((r) => r.name === name) ?? { name, kinds: [kind], engine: "" };
  // Same identity as Task.CheckerGroup: work kind + member, or normalized seat.
  const checkerGroup = (name: string) => {
    const role = team.find((r) => r.name === name);
    const seat = name.trim().toLowerCase();
    return role
      ? `${workingKind(role)}/${role.member ? `member/${role.member}` : `seat/${seat}`}`
      : `/seat/${seat}`;
  };
  const rows: FlowRow[] = [];
  const kinds = [
    ...(task.status === "triage" ? [["pm", "Triage"]] : []),
    ["researcher", "Research"],
    ["designer", "Design"],
    ["implementer", build],
    ["reviewer", "Review"],
    ["qa", "QA"],
  ];
  for (const [kind, label] of kinds) {
    const seats = team.filter((r) => holds(r, kind));
    const step =
      kind === "implementer"
        ? "writing"
        : kind === "researcher"
          ? "researching"
          : kind === "designer"
            ? "designing"
            : kind === "pm"
              ? "triage"
              : "reviewing";
    const working = active
      .filter((r) => holds(r, kind))
      .filter((r) => {
        if (kind === "researcher" && task.with_designer) return false;
        const turns = state.turns.filter(
          (t) => t.task_id === task.id && t.seat === r.name,
        );
        if (turns.length) return turns.some((t) => t.role === kind);
        return (task.claims ?? []).some(
          (c) => c.seat === r.name && !c.held && c.step === step,
        );
      });
    // Held claims name the seat that will resume, but never count as live work.
    const heldSeat = task.claims?.find(
      (c) => c.held && c.step === step && seats.some((r) => r.name === c.seat),
    )?.seat;
    const atWork = roleAtWork({ ...task, roles: team });
    const historicalSeats = [...seats, ...pinned.filter((r) => holds(r, kind))];
    const lastName =
      kind === "researcher"
        ? task.plan?.role
        : kind === "designer"
          ? design?.designer
          : kind === "reviewer" || kind === "qa"
            ? verdicts.find((v) =>
                historicalSeats.some((r) => r.name === v.role),
              )?.role
            : undefined;
    const selected = working.length
      ? working
      : [
          (currentKind === kind || (stopped && heldSeat)
            ? heldSeat
              ? lookup(heldSeat, kind)
              : atWork && holds(atWork, kind)
                ? atWork
                : undefined
            : undefined) ?? (lastName ? lookup(lastName, kind) : seats[0]),
        ].filter((r): r is Role => !!r);
    // A message recipient must remain reachable alongside the selected seat.
    for (const seat of seats) {
      if (
        task.messages?.some((m) => m.to === seat.name && isOpenMessage(m)) &&
        !selected.some((r) => r.name === seat.name)
      )
        selected.push(seat);
    }
    if (
      askingKind === kind &&
      design?.from &&
      !selected.some((r) => r.name === design.from)
    )
      selected.push(lookup(design.from, kind));
    if (!selected.length) continue;
    for (const role of selected) {
      const row: FlowRow = {
        key: `${kind}:${role.name}`,
        kind,
        label,
        role,
        state: "not-started",
        support: "",
        previous: [],
      };
      const who = role.name;
      let done = "";
      if (kind === "pm")
        row.support = `${who} will start when this request begins`;
      if (kind === "researcher") {
        row.support = `${who} will start when this request begins`;
        if (task.plan && (!pass || pass.answered_at))
          done = `Research recorded${task.plan.at ? ` ${sinceLabel(task.plan.at)}` : ""}`;
        if (research.length) {
          row.roundLabel = `pass ${research.length + 1}`;
          row.previous = [
            ...(task.plan
              ? [{ label: "Research pass 1", outcome: "Research recorded" }]
              : []),
            ...research.flatMap((r, i) =>
              i < research.length - 1 && r.answered_at
                ? [
                    {
                      label: `Research pass ${i + 2}`,
                      outcome: "Research recorded",
                      at: r.answered_at,
                    },
                  ]
                : [],
            ),
          ].reverse();
        }
      } else if (kind === "designer") {
        row.support = `${who} will give design input if asked`;
        if (design?.answered_at)
          done = `Design input recorded ${sinceLabel(design.answered_at)}`;
        else if (!design && (latest || finished(task)))
          row.support = `${who} wasn't asked for design input on this request`;
        row.previous = designs
          .slice(0, -1)
          .reverse()
          .flatMap((d, i) =>
            d.answered_at
              ? [
                  {
                    label: `Design ${d.n ?? designs.length - i - 1}`,
                    outcome: "Design input recorded",
                    at: d.answered_at,
                  },
                ]
              : [],
          );
        if (designs.length > 1)
          row.roundLabel = `input ${design?.n ?? designs.length}`;
      } else if (kind === "implementer") {
        row.support = `${who} will ${build.toLowerCase()} when ${team.some((r) => holds(r, "researcher")) ? "research is done" : "this request begins"}`;
        if (n > 1) row.roundLabel = `round ${n}`;
        if (latest?.n === n)
          done = `${build} ${n} made${latest.at ? ` ${sinceLabel(latest.at)}` : ""}`;
        row.previous = revisions
          .filter((r) => r.n < n)
          .map((r) => ({
            label: `${build} ${r.n}`,
            outcome: r.summary || `${build} made`,
            at: r.at,
          }));
      } else if (kind === "reviewer" || kind === "qa") {
        row.roundLabel = `${artifact} ${n}`;
        row.support = `${who} will check ${artifact} ${n} when it is ready`;
        const own = verdicts.filter(
          (v) => checkerGroup(v.role) === checkerGroup(who),
        );
        const verdict =
          latest?.n === n
            ? own.find(
                (v) =>
                  v.revision === n &&
                  v.brief_version === project.brief.version &&
                  (v.text_version ?? 0) === (task.text_version ?? 0) &&
                  !v.answered,
              )
            : undefined;
        if (verdict)
          done = `${verdictOutcome[verdict.outcome]?.label ?? verdict.outcome} ${artifact} ${n}${verdict.at ? ` · ${sinceLabel(verdict.at)}` : ""}`;
        row.previous = own
          .filter((v) => v.revision < n)
          .map((v) => ({
            label: `${label} · ${artifact} ${v.revision}`,
            outcome: verdictOutcome[v.outcome]?.label ?? v.outcome,
            at: v.at,
          }));
      }
      const current = currentKind === kind && selected[0]?.name === who;
      const asking = askingKind === kind && design?.from === who;
      const running =
        !stopped &&
        !finished(task) &&
        !queued &&
        (asking ||
          working.some((r) => r.name === who) ||
          (current &&
            (!done ||
              task.waiting ||
              task.status === "writing" ||
              task.status === "researching" ||
              task.status === "reviewing" ||
              task.with_designer ||
              heldSeat === who ||
              task.checking === who)));
      if (done) {
        row.state = "done";
        row.support = done;
      }
      if (running || (kind === "pm" && task.status === "triage")) {
        row.state = "working";
        const turn = state.turns.find(
          (t) => t.task_id === task.id && t.seat === who && t.role === kind,
        );
        row.support = asking
          ? `Waiting for ${design?.designer ?? "the designer"} to give design input`
          : turn
            ? workingParts(turn, Date.now(), false).join(" · ")
            : task.waiting
              ? waitingWords(task, task.waiting, project)
              : task.detail ||
                waitingLine(
                  { ...task, roles: team, checking: who },
                  state,
                  Date.now(),
                ) ||
                `Waiting for ${who} to pick this up`;
      } else if (queued) {
        row.state = "not-started";
        row.support = `${who} will start when this request begins`;
      }
      if (kind === "implementer" && row.state === "done" && seats.length > 1)
        row.support += " · Builder not recorded";
      if (
        stopped &&
        (current ||
          asking ||
          working.some((r) => r.name === who) ||
          heldSeat === who) &&
        !done
      ) {
        row.state = "interrupted";
        row.support =
          kind === "implementer"
            ? `Stopped before ${artifact} ${n} was made`
            : `Stopped before ${label.toLowerCase()} was completed`;
      }
      rows.push(row);
    }
  }
  if (task.status === "landing" || task.status === "awaiting")
    rows.push({
      key: "landing",
      kind: "landing",
      label: "Landing",
      state: "working",
      support:
        task.status === "awaiting"
          ? task.proposal?.number
            ? `Pull request #${task.proposal.number}: ${prWords(task.proposal.observed)}`
            : "Waiting on checks and reviews"
          : "Landing",
      previous: [],
    });
  const currentRows = rows.filter(
    (r) => r.kind === currentKind || r.kind === "landing",
  );
  const affected = currentRows.length
    ? currentRows
    : [
        rows.find((r) => r.state === "working" || r.state === "interrupted") ??
          (task.stage === "ready"
            ? [...rows].reverse().find((r) => r.state === "done")
            : undefined) ??
          rows.find(
            (r) => r.state === "not-started" && r.kind !== "designer",
          ) ??
          rows.at(-1),
      ].filter((r): r is FlowRow => !!r);
  if (stopped) {
    rows.forEach((row) => {
      if (row.state === "not-started")
        row.support = "Will not run: this request was stopped.";
    });
  }
  const decision = decisionFor(task, state.decisions);
  const blocker = task.blockers?.find((b) => !b.cleared_at);
  const stoppedRow =
    affected.find((r) => r.state === "interrupted") ?? affected[0];
  for (const row of affected) {
    if (stopped) {
      if (row === stoppedRow)
        row.exception = { tone: "stopped", text: "Stopped" };
    } else if (decision?.kind === "failure" || blocker)
      row.exception = {
        tone: "block",
        text:
          blocker?.description ||
          task.detail ||
          decision?.context ||
          decisionKind(decision).step(task),
      };
    else if (needsYou(task))
      row.exception = {
        tone: "needs",
        text: decisionKind(decision).step(task),
      };
  }
  return rows;
}

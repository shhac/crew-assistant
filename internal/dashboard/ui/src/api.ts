import type { Connection } from "./ConnectionsSettings";
export interface AvatarMark {
  d: string;
  color: string;
  stroke_width: number;
}
export interface AvatarSpec {
  shape?: string;
  background?: string;
  accent?: string;
  marks?: AvatarMark[];
  /** A picture Codex drew, by its id; shown in place of the vector sketch. */
  image?: string;
  /** How the picture was described when it was drawn. */
  look?: string;
}
/** Anyone with a face: the assistant, a member or a proposed identity. */
export interface Face {
  avatar?: AvatarSpec;
  avatar_svg?: string;
}
/** Anyone Codex draws: the assistant or a member. */
export interface Drawable extends Face {
  /** Codex is drawing its picture. */
  drawing?: boolean;
  /** Why the last drawing failed. */
  draw_error?: string;
}
export interface Brief {
  version: number;
  goal: string;
  audience?: string;
  constraints?: string;
  criteria: string[] | null;
  updated_at?: string;
}
export interface BriefInput {
  goal: string;
  audience: string;
  constraints: string;
  criteria: string[];
}
export interface Role {
  provider?: string;
  name: string;
  /**
   * At most one of implementer, reviewer and QA, and perhaps researcher,
   * designer and PM.
   */
  kinds: (MemberKind | (string & {}))[];
  engine: string;
  model?: string;
  effort?: string;
  instructions?: string;
  /** The member this role was copied from, if any. */
  member?: string;
  /** Whether QA in this seat uses its engine's browser. */
  browser?: Browser;
}
/**
 * A member, QA or the assistant using the browser its engine ships: the
 * owner's real Chrome, with its logins. An empty name is the browser the
 * extension connects by default.
 */
export interface Browser {
  on?: boolean;
  name?: string;
}
/** How QA starts a code project's app to use it, on this machine only. */
export interface RunRecipe {
  setup?: string;
  start: string;
  /** Where it answers, with {port} where each check's port goes. */
  url: string;
  /** A command that succeeds once it is ready; empty waits for the URL. */
  ready?: string;
}
export type MemberKind =
  "researcher" | "designer" | "implementer" | "reviewer" | "qa" | "pm";
export interface Learning {
  id: string;
  /** The situation it applies to, like a skill's description. */
  when?: string;
  text: string;
  source?: "owner" | "assistant" | "member";
  project_id?: string;
  task_id?: string;
  at?: string;
}
export interface LearningInput {
  when: string;
  text: string;
  project_id: string;
}
export interface Member extends Drawable {
  provider?: string;
  id: string;
  name: string;
  kinds: MemberKind[];
  engine: string;
  model?: string;
  effort?: string;
  instructions?: string;
  description?: string;
  /** How they write. */
  personality?: string;
  browser?: Browser;
  learnings: Learning[];
  created_at?: string;
}
export interface MemberInput {
  provider?: string;
  name: string;
  kinds: MemberKind[];
  engine: string;
  model: string;
  effort: string;
  instructions: string;
  description: string;
  personality: string;
  browser: Browser;
  /** A new member's stand-in face and the look to draw, from a suggestion. */
  avatar?: AvatarSpec;
}
/** An assistant's model: any engine, the API one included. */
export interface AssistantModel {
  engine: string;
  /** The API provider a model on another API runs on; absent is the first. */
  provider?: string;
  model: string;
  effort: string;
  max_tokens: number;
}
/** One of the owner's assistants; the one in the seat is the assistant. */
export interface AssistantProfile extends Drawable {
  id: string;
  name: string;
  personality: string;
  model: AssistantModel;
  browser?: Browser;
}
export interface AssistantInput {
  name: string;
  personality: string;
  model: AssistantModel;
  browser?: Browser;
  /** A new assistant's stand-in face and the look to draw, from a suggestion. */
  avatar?: AvatarSpec;
}
export interface ReleasePolicy {
  when: string;
  check?: string;
  github?: string;
  approve?: string;
}
export interface ReleaseRecord {
  version: string;
  commit: string;
  notes: string;
  at: string;
  approved_by?: string;
  published?: string;
  note?: string;
}
export interface Playbook {
  release?: ReleasePolicy;
  template: string;
  medium: string;
  roles: Role[];
  max_rounds: number;
  deliver: string;
  deliver_to?: string;
  repo?: string;
  branch_prefix?: string;
  check?: string;
  prepare?: string[];
  sign?: string;
  /** QA runs the check in a writable copy of the revision. */
  check_in_copy?: boolean;
  /** QA's check may bind and reach this machine's own addresses. */
  check_loopback?: boolean;
  land?: LandPolicy;
  /** How QA starts the app to use it; none keeps QA to the check. */
  run?: RunRecipe;
  /**
   * How many tasks may be under way at once; absent is one per implementer
   * seat.
   */
  max_active?: number;
  /** The most requests each working stage may hold; a stage absent has no limit. */
  stage_limits?: Partial<Record<Stage, number>>;
}
function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
/** section reads one group of the loosely typed config, or an empty one. */
export function section(value: unknown): Record<string, unknown> {
  return isRecord(value) ? value : {};
}
/**
 * withSettings merges fields into a group, dropping any left blank so the
 * server applies its default.
 */
export function withSettings(
  value: unknown,
  fields: Record<string, unknown>,
): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries({ ...section(value), ...fields }).filter(
      ([, v]) => v !== undefined && v !== "",
    ),
  );
}
/** withEngine merges fields into one engine's settings, keeping the rest. */
export function withEngine(
  config: Config,
  engine: string,
  fields: Record<string, unknown>,
): Config {
  const engines = section(config.engines);
  return {
    ...config,
    engines: { ...engines, [engine]: withSettings(engines[engine], fields) },
  };
}
export interface LandPolicy {
  means?: string;
  /** How a change lands without pull requests. */
  via?: "branch" | "push" | (string & {});
  target?: string;
  /** fast-forward, for a push. */
  method?: string;
  /** Lands each change through a GitHub pull request into target instead. */
  pull_requests?: boolean;
  github?: string;
  /** squash, merge or rebase, for a pull request. */
  merge?: string;
  /** Who decides that a pull request opens: pm (the default), owner or implementer. */
  open?: "pm" | "owner" | "implementer" | (string & {});
  /**
   * Who approves a change landing: before (the owner), none, or pm (the
   * team's PM). With pull requests it is who approves merging, pm unless set.
   */
  approve?: "before" | "none" | "pm" | (string & {});
}
/** The PM's decision to land or hold a task's change, and why. */
export interface LandDecision {
  by: string;
  land: boolean;
  /** squash (one commit) or fast-forward (the task's own commits). */
  method?: "squash" | "fast-forward" | (string & {});
  reason: string;
  revision: number;
  at: string;
}
export interface LandingInput {
  means: string;
  via: string;
  target: string;
  method: string;
  pull_requests: boolean;
  github: string;
  merge: string;
  open: string;
  approve: string;
}
export interface LinearLink {
  connection_id: string;
  profile: string;
  kind: "team" | "project";
  id: string;
  name: string;
  rules: {
    pick_up: boolean;
    states: string[];
    assignee: "any" | "unassigned" | "me" | "users";
    users: { id: string; name: string }[];
  };
  version?: number;
  cursor?: string;
  last_error?: string;
}
export interface LinearRef {
  description?: string;
  kind: "issue" | "project";
  connection_id: string;
  profile: string;
  id: string;
  identifier: string;
  title: string;
  url: string;
  by: string;
  at: string;
}
export interface Project {
  releases?: ReleaseRecord[];
  paused?: boolean;
  /** Landing held, as for a code freeze, while other work goes on. */
  landing_paused?: { reason?: string; at: string };
  id: string;
  title: string;
  /** Starts the readable IDs of the project's tasks, as CA does CA-12. */
  prefix?: string;
  status: string;
  brief: Brief;
  playbook?: Playbook;
  directories?: string[];
  scratch_directory?: string;
  source_id?: string;
  source_description?: string;
  /** Optional per-project intake through the configured Linear CLI account. */
  linear?: LinearLink;
  /** Who last set the to-do order. */
  ordered_by?: "owner" | "assistant" | "pm" | (string & {});
  ordered_at?: string;
  /** The team's PM is to look at the to-do list next. */
  pm_due?: boolean;
  /** What the owner told the PM, for its next look. */
  pm_direction?: string;
  updated_at?: string;
}
export interface ProjectInput {
  title: string;
  directories: string[];
  brief: BriefInput;
  template: string;
}
export interface TeamInput {
  template: string;
  writer_engine: string;
  reviewer_engine: string;
  implementer_member?: string;
  reviewer_member?: string;
  qa_member?: string;
  /** "" keeps the template's researcher, "none" leaves research out. */
  researcher_member?: string;
  /** The member who gives design input when asked, or "" for none. */
  designer_member?: string;
  /** The member who keeps the to-do list in order, or "" for no PM. */
  pm_member?: string;
  max_rounds: string;
  deliver_to: string;
  repo?: string;
  branch_prefix?: string;
  check?: string;
  prepare?: string[];
  sign?: string;
  /** "yes" or "no"; absent keeps the team's choice. */
  check_loopback?: "yes" | "no";
}
export interface WorkspaceInput {
  repo: string;
  branch_prefix: string;
  prepare: string[];
  sign: string;
}
export interface TaskInput {
  depends_on?: string[];
  owner_checks?: string[];
  objective: string;
  criteria: string[];
}
export type TaskStatus =
  | "queued"
  | "triage"
  | "researching"
  | "designing"
  | "writing"
  | "reviewing"
  | "deciding"
  | "waiting"
  | "delivered"
  | "landing"
  | "awaiting"
  | "landed"
  | "stopped";
export type Stage =
  | "todo"
  | "triage"
  | "researching"
  | "designing"
  | "implementing"
  | "reviewing"
  | "qa"
  | "pr_opening"
  | "pr_open"
  | "ready"
  | "done"
  | "stopped";
export interface Revision {
  seat?: string;
  n: number;
  brief_version: number;
  files: string[] | null;
  ref?: string;
  clean_merge_of?: number;
  /** "owner" for a draft the owner made by hand. */
  by?: string;
  summary?: string;
  at?: string;
}
export interface Finding {
  criterion?: string;
  note: string;
}
export interface Verdict {
  text_version?: number;
  revision: number;
  /** Exactly what was checked: the commit, or a document draft's digest. */
  ref?: string;
  role: string;
  brief_version: number;
  outcome: "pass" | "revise" | "question" | "research" | (string & {});
  summary: string;
  findings?: Finding[];
  question?: string;
  asked?: string;
  /** Where the checker recommends the task goes next, and a line on why. */
  next?: "land" | "revise" | "research" | (string & {});
  note?: string;
  /** Its question or research request came back; it judged the draft again. */
  answered?: boolean;
  /** Names a verdict whose screenshots are kept with the task. */
  id?: string;
  /** What QA saw using the app. */
  evidence?: Evidence[];
  at?: string;
}
/**
 * One thing QA saw using the app: a screenshot, kept as one of the task's
 * attachments, or a finding in words.
 */
export interface Evidence {
  kind: "screenshot" | "console" | "network" | "page" | (string & {});
  text?: string;
  attachment?: string;
}
/** A checker sending the task back to the researcher, and back again. */
export interface ResearchRequest {
  id: string;
  from: string;
  round: number;
  revision: number;
  question: string;
  researcher?: string;
  /** The owner's decision it went to instead, past the limit. */
  decision?: string;
  at?: string;
  answered_at?: string;
}
export interface TaskText {
  owner_checks?: string[];
  objective: string;
  criteria: string[] | null;
}
/** One change the team or the owner made to a task's title or requirements. */
export interface TaskEdit {
  id: string;
  /** Who made it: a seat's name, or "owner". */
  by: string;
  /** The role it was made as, or "owner". */
  kind: string;
  before: TaskText;
  after: TaskText;
  /** The edit this one undoes. */
  undoes?: string;
  at: string;
}
/** A note left on a task for whoever works on it. */
export interface Note {
  id: string;
  by: string;
  kind: string;
  text: string;
  at: string;
}
/**
 * A file kept with a task: the owner's, with a note, the designer's, with a
 * design, or QA's screenshot, with its verdict. Exactly one of note, design
 * and verdict is set.
 */
export interface Attachment {
  id: string;
  name: string;
  /** Judged from the file's bytes, never from what the sender said. */
  type: string;
  size: number;
  by: string;
  kind: string;
  at: string;
  note?: string;
  design?: string;
  verdict?: string;
}
export interface TeamMessage {
  id: string;
  to: string;
  kind: string;
  from: "owner" | "assistant" | (string & {});
  text: string;
  status: "waiting" | "working" | "answered" | "failed" | "closed";
  reply?: string;
  outcome?: string;
  revision?: number;
  /** Which of the task's direction entries this message became. */
  direction: number;
  at?: string;
  answered_at?: string;
  /** Asked by a teammate, named in from, about the pull request; the reply is posted there. */
  for_pr?: boolean;
}
/** What the researcher worked out before anything was written. */
export interface Plan {
  owner_checks?: string[];
  needs_designer?: string;
  summary: string;
  exists?: string[];
  changes?: string[];
  /** What the record must say if a step stops part-way. */
  failure_paths?: string[];
  tests?: string[];
  out_of_scope?: string[];
  questions?: string[];
  /** Parts left for later, each queued as a task that waits for this one. */
  split_off?: { objective: string; task?: string }[];
  /** The seat that researched it. */
  role: string;
  at: string;
  prerequisites?: {
    what: string;
    blocker: string;
    outcome?: "confirmed" | "dropped";
  }[];
}
/** One hand-off to the designer, and the input that came back. */
export interface DesignRequest {
  id: string;
  /** Numbers the designer's input: design 1, 2, … */
  n?: number;
  /** Made the current design at some point: a design, not advice. */
  marked?: boolean;
  /** The seat that asked, and the step it asked from. */
  from: string;
  step: "researching" | "writing" | (string & {});
  round: number;
  question: string;
  designer?: string;
  input?: string;
  /** The owner's decision it went to instead, past the limit or escalated. */
  decision?: string;
  at?: string;
  answered_at?: string;
}
export type Relation = "depends_on" | "blocks" | "relates_to";
export interface LinkMark {
  /** "owner", "assistant", "pm", "member:<id>" or "role:<kind>". */
  by: string;
  at: string;
}
export interface Proposal {
  branch: string;
  number?: number;
  url?: string;
  /** What the loop last saw of the open pull request. */
  observed?: Observed;
  /** The implementer's round on what the pull request asked for. */
  answering?: boolean;
  /** The revision approved to merge once its pull request is ready. */
  merge_approved?: number;
}
/** An open pull request as the loop last saw it. */
export interface Observed {
  checks: "SUCCESS" | "FAILURE" | "PENDING" | "NONE" | (string & {});
  /** GitHub's review decision; empty where the repository asks for none. */
  review?: string;
  unresolved?: number;
  conflicting?: boolean;
  ready?: boolean;
  /** Feedback from people outside the repository, never acted on. */
  ignored?: number;
  at: string;
}
/** Who or what a task's ready step waits for. */
export interface Wait {
  kind:
    | "member"
    | "project_cap"
    | "engine_cap"
    | "engine_paused"
    | "owner"
    | "stage"
    | "blocker";
  until?: string;
  /** The busy person's seat, and the member it is filled from, if any. */
  seat?: string;
  member?: string;
  /** The readable ID of the busy task or full stage's sole holder. */
  on?: string;
  /** Objective of the busy task or the sole stage holder. */
  objective?: string;
  /** Title of the busy task's project, only when it differs. */
  project?: string;
  /** The project whose to-do list they are busy with instead. */
  list?: string;
  active?: number;
  cap?: number;
  engine?: string;
  /**
   * The stage it waits for room in, the stage it holds meanwhile (none from
   * To do), and how many the full stage holds against its limit.
   */
  stage?: Stage;
  from?: Stage;
  count?: number;
  limit?: number;
}
/**
 * A step of a task a seat has taken and not yet finished. Checks of one
 * draft are shared, so a reviewer and QA can each hold one at once.
 */
export interface Claim {
  /** The task status the step is for, or "message" or "adopt". */
  step: string;
  /** Empty for a step no seat takes, such as deciding or landing. */
  seat?: string;
  shared?: boolean;
  /** Why a turn a stopped daemon left behind is held, if it is. */
  held?: string;
}

export interface Blocker {
  id: string;
  kind: "manual" | "daemon_includes" | "prerequisite";
  outcome?: "confirmed" | "dropped";
  description: string;
  task?: string;
  landing_only?: boolean;
  by: string;
  at: string;
  cleared_at?: string;
  cleared_by?: string;
  check?: string;
  answer_pending?: boolean;
}
export interface NextTaker {
  kind: string;
  group?: string;
  preferred?: string;
  did?: string;
  others?: string[];
}
export interface Task {
  owner_checks?: string[];
  takes?: NextTaker[];
  blockers?: Blocker[];
  /** Original source issues and their context for the offline team. */
  linear?: LinearRef[];
  linear_links?: LinearRef[];
  id: string;
  project_id: string;
  /** The task's readable ID, such as CA-12; empty before it has one. */
  ref?: string;
  number?: number;
  objective: string;
  criteria: string[] | null;
  status: TaskStatus;
  stage: Stage;
  /**
   * The working stage it holds towards the project's stage limits: the one
   * it last entered, kept while it waits for room in the next.
   */
  place?: Stage;
  /**
   * The checker at work while it is checked, the researcher while it
   * researches, or the designer while it gives design input.
   */
  checking?: string;
  /** With the designer for design input, in the stage of whoever asked. */
  with_designer?: boolean;
  text_version?: number;
  /** Waiting on a decision the owner has already made; it resumes next. */
  answered?: boolean;
  detail?: string;
  /** Who or what the next step waits for, while it is ready and can't start. */
  waiting?: Wait;
  /** The steps seats have taken and not yet finished. */
  claims?: Claim[];
  roles?: Role[];
  playbook?: Playbook;
  max_rounds?: number;
  round: number;
  direction?: string[];
  direction_pending?: number;
  messages?: TeamMessage[];
  plan?: Plan;
  design?: DesignRequest[];
  /** The design request whose input is the current design, the target. */
  current_design?: string;
  research?: ResearchRequest[];
  edits?: TaskEdit[];
  notes?: Note[];
  attachments?: Attachment[];
  /** Ids of the tasks that must land before this one starts. */
  depends_on?: string[];
  /** Unfinished dependencies with their current project and readable ID. */
  waiting_on?: {
    task: string;
    ref: string;
    project_id: string;
    project: string;
    objective: string;
  }[];
  /** The objectives of its unfinished dependencies. */
  waits_for?: string[];
  /** Ids of the tasks that depend on this one. */
  blocks?: string[];
  relates_to?: string[];
  /**
   * Who set each link, keyed "depends_on:<id>" or "relates_to:<id>". A link
   * with no entry was set by the team before links were marked.
   */
  linked_by?: Record<string, LinkMark>;
  proposal?: Proposal;
  branch?: string;
  retry_at?: string;
  revisions: Revision[] | null;
  verdicts: Verdict[] | null;
  decision_id?: string;
  delivered_to?: string;
  land_decision?: LandDecision;
  /** Signed off and waiting only on the PM's decision; the owner may land it. */
  pm_deciding?: boolean;
  created_at?: string;
  updated_at?: string;
}
export interface RevisionFile {
  path: string;
  content?: string;
  binary?: boolean;
  truncated?: boolean;
  size: number;
}
export type DecisionKind =
  | "choice"
  | "delivery"
  | "update"
  | "question"
  | "escalation"
  | "failure"
  | "pm-question"
  | "release"
  | "release-failed";
export interface Decision {
  answer?: string;
  disposition?: "choice" | "custom" | "dismissed" | "completed" | "superseded";
  resolution_reason?: string;
  resolved_at?: string;
  id: string;
  project_id?: string;
  task_id?: string;
  kind?: DecisionKind | (string & {});
  title: string;
  context: string;
  recommendation: string;
  choices: string[];
  status: string;
  created_at?: string;
}
export interface Message {
  id: string;
  role: string;
  content: string;
  origin?: "wake" | "overview" | (string & {});
  created_at?: string;
}
export interface ChatToolEvent {
  id: string;
  tool: string;
  label: string;
  status: "running" | "completed" | "failed" | "interrupted";
  started_at: string;
  finished_at?: string;
}
export interface ChatTurn {
  id: string;
  message: string;
  status:
    "queued" | "running" | "completed" | "failed" | "interrupted" | "cancelled";
  created_at: string;
  started_at?: string;
  finished_at?: string;
  user_message_id?: string;
  assistant_message_id?: string;
  error?: string;
  loading_phrase?: string;
  model_status?: string;
  browser_note?: string;
  retry_at?: string;
  revision: number;
  events: ChatToolEvent[];
  /** The chat command this turn runs instead of a reply, such as "compact". */
  command?: string;
  /** What a command did, in a line. */
  outcome?: string;
  /** "assistant" for a command the assistant asked for itself. */
  origin?: string;
}
/**
 * The model session the current conversation runs on, when its engine keeps
 * one; a conversation run turn by turn has none.
 */
export interface ChatSession {
  engine: string;
  model: string;
  started_at: string;
  /** How it was last opened: new, picked up again, or new because the old one couldn't be resumed. */
  opened: "fresh" | "resumed" | "rebuilt";
  seen_at?: string;
  compactions?: number;
  /** Tokens in the model's context after the latest turn. */
  context_used?: number;
  context_window?: number;
  /** The latest turn's input tokens, and how many the provider served from cache. */
  input?: number;
  cached_input?: number;
  updated_at: string;
}
/** A past conversation, archived by /new or /clear. */
export interface ConversationEntry {
  id: string;
  title: string;
  started_at: string;
  archived_at: string;
  messages: number;
}
export interface Conversation extends Omit<ConversationEntry, "messages"> {
  messages: Message[];
}
export function listConversations() {
  return api<{ conversations: ConversationEntry[] }>("/api/chat/conversations");
}
export function getConversation(id: string) {
  return api<Conversation>(`/api/chat/conversations/${encodeURIComponent(id)}`);
}
export function resumeConversation(id: string) {
  return api(`/api/chat/conversations/${encodeURIComponent(id)}/resume`, {
    method: "POST",
  });
}
export interface Memory {
  id: string;
  key?: string;
  content: string;
  kind?: string;
  source?: string;
  /** The assistant a memory it kept about itself belongs to; none is about the owner. */
  assistant?: string;
  supersedes?: string;
  superseded_at?: string;
  updated_at?: string;
}
export interface Activity {
  id: string;
  project_id?: string;
  /** The task an entry is about, where it is about one. */
  task_id?: string;
  kind?: string;
  summary: string;
  created_at?: string;
}
export interface Integration {
  id: string;
  project_id?: string;
  name: string;
  status: string;
  detail?: string;
}
export interface PendingOperation {
  id: string;
  summary: string;
  project_id?: string;
}
/** A role at work right now; the loop runs one at a time. */
export interface Turn {
  project_id: string;
  /** Absent for a PM looking at the to-do list. */
  task_id?: string;
  role: MemberKind;
  seat: string;
  member?: string;
  started_at: string;
  /** When its session last reported anything. */
  last_activity_at: string;
  tool_calls: number;
  edits: number;
  /** The tool running now, if one is. */
  tool?: string;
  output_tokens: number;
  /** How many files differ from where the round started, for a turn that writes. */
  files_changed?: number;
}
/**
 * One thing a seat did on a request, as its session reported it: the prompt
 * it was given, a reply it wrote, or a tool it ran. A reply streaming in or a
 * tool still running is the same step, changed as it goes.
 */
export interface TurnStep {
  id: number;
  task_id: string;
  seat: string;
  member?: string;
  role: string;
  /** The run it belongs to. */
  turn: string;
  item: string;
  at: string;
  kind: "prompt" | "reply" | "tool" | "note";
  text?: string;
  tool?: string;
  /** The tool's arguments, as JSON when they weren't cut. */
  input?: string;
  output?: string;
  /** For a tool: running, completed, failed, interrupted or the engine's own word. */
  status?: string;
  exit_code?: number;
  /** Some of it was cut to keep it to a sensible size. */
  clipped?: boolean;
}
export interface EnginePause {
  at: string;
  until?: string;
}
export function enginePaused(
  pauses: Record<string, EnginePause> | undefined,
  engine: string,
) {
  const pause = pauses?.[engine];
  return pause && (!pause.until || Date.parse(pause.until) > Date.now())
    ? pause
    : undefined;
}
export function setEnginePaused(
  engine: string,
  paused: boolean,
  until?: string,
) {
  return api<{
    engine: string;
    state: "running" | "paused" | "held";
    until?: string;
    reason?: string;
  }>(`/api/engines/${encodeURIComponent(engine)}/paused`, {
    method: "PUT",
    body: JSON.stringify({ paused, until }),
  });
}
export interface UpdateStatus {
  running?: string;
  available?: string;
  notes?: string;
  url?: string;
  checked_at?: string;
  error?: string;
  skipped?: string;
  decision_version?: string;
  mode?: "off" | "ask";
  unavailable?: string;
}
export interface State {
  engine_pauses?: Record<string, EnginePause>;
  update?: UpdateStatus;
  pending_operations: PendingOperation[];
  /** The assistant in the seat; with no one there it has no id. */
  assistant: Drawable & {
    engine?: string;
    id?: string;
    name: string;
    personality: string;
    theme?: string;
  };
  assistants: AssistantProfile[];
  projects: Project[];
  members: Member[];
  tasks: Task[];
  decisions: Decision[];
  messages: Message[];
  memories: Memory[];
  activity: Activity[];
  integrations: Integration[];
  turns: Turn[];
  paused: boolean;
  // The daemon is finishing the work in progress before it stops.
  stopping: boolean;
  demo: boolean;
}
export type Config = Record<string, unknown> & {
  upgrade?: {
    mode: "off" | "ask";
    check_interval?: string;
    source_repo?: string;
    formula?: string;
    release_api_url?: string;
    formula_url?: string;
  };
  assistant?: {
    /** The id of the assistant in the seat; empty is no one. */
    seat?: string;
    theme?: string;
    [key: string]: unknown;
  };
  assistants?: AssistantProfile[];
  connections?: Connection[];
  /**
   * Keyed by engine: any CLI engine (bin, home, usage_floor,
   * on_unknown_usage) and "openai-compatible" (base_url, api_key_env,
   * effort_parameter); "providers" holds the named API providers.
   */
  engines?: Record<string, unknown>;
};
/** Where an API reads reasoning effort; blank is top-level reasoning_effort. */
export type EffortParameter = "" | "reasoning_effort" | "reasoning.effort";
/** A named OpenAI-compatible endpoint beside the single API setting. */
export interface APIProviderSettings {
  id: string;
  name: string;
  base_url: string;
  api_key_env: string;
  effort_parameter?: EffortParameter;
}
/** The id the single API setting has among the providers. */
export const legacyProvider = "openai-compatible";
/** An API provider a model can run on, as the saved settings name it. */
export interface ProviderChoice {
  id: string;
  label: string;
}
export function getProviders() {
  return api<ProviderChoice[]>("/api/providers");
}
export class APIError extends Error {
  constructor(
    message: string,
    public status: number,
  ) {
    super(message);
  }
}
export async function api<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  // A form sets its own type, with the boundary between its parts.
  const form = options.body instanceof FormData;
  const response = await fetch(path, {
    ...options,
    credentials: "same-origin",
    headers: {
      ...(form ? {} : { "Content-Type": "application/json" }),
      "X-Requested-With": "crew-assistant",
      ...options.headers,
    },
  });
  const body = await response.json().catch(() => null);
  if (!response.ok) {
    const detail = body?.error;
    const message = typeof detail === "string" ? detail : detail?.message;
    throw new APIError(
      [message || `Something went wrong (${response.status})`, body?.hint]
        .filter(Boolean)
        .join(" "),
      response.status,
    );
  }
  return body as T;
}
export function normalizeState(raw: Partial<State>): State {
  return {
    update: raw.update,
    assistant: raw.assistant ?? { name: "", personality: "" },
    assistants: raw.assistants ?? [],
    pending_operations: raw.pending_operations ?? [],
    projects: raw.projects ?? [],
    members: (raw.members ?? []).map((m) => ({
      ...m,
      learnings: m.learnings ?? [],
    })),
    tasks: raw.tasks ?? [],
    decisions: raw.decisions ?? [],
    messages: raw.messages ?? [],
    memories: raw.memories ?? [],
    activity: raw.activity ?? [],
    integrations: raw.integrations ?? [],
    turns: raw.turns ?? [],
    paused: raw.paused ?? false,
    engine_pauses: raw.engine_pauses ?? {},
    stopping: raw.stopping ?? false,
    demo: raw.demo ?? false,
  };
}
// pendingDecisions are the ones still waiting on the owner: every decision
// the daemon has not closed as resolved or dismissed.
export function pendingDecisions(decisions: Decision[]) {
  return decisions.filter(
    (d) => d.status !== "resolved" && d.status !== "dismissed",
  );
}
export function errorText(error: unknown) {
  return error instanceof Error
    ? error.message
    : "Something went wrong. Try again.";
}
export function criteriaLines(criteria: string[] | string | null): string[] {
  return (Array.isArray(criteria) ? criteria : (criteria || "").split("\n"))
    .map((s) => s.trim())
    .filter(Boolean);
}

const projectPath = (projectID: string) =>
  `/api/projects/${encodeURIComponent(projectID)}`;

export function getState() {
  return api<State>("/api/state");
}
export type UsageLevel = "unknown" | "ok" | "low" | "exhausted";
/** One of an engine's usage windows, as its CLI measured it. */
export interface UsageWindow {
  name: string;
  left_percent: number;
  /** The owner's floor for this window; below it team work is held. */
  floor_percent: number;
  /** Only when the CLI said. */
  resets_at?: string;
  level: UsageLevel;
}
/** What one engine's login reports it has left. */
export interface EngineUsage {
  as_of?: string;
  engine: string;
  /**
   * An API provider, listed only while it rests for its rate limit, and
   * how it is named.
   */
  provider?: string;
  label?: string;
  level: UsageLevel;
  windows: UsageWindow[];
  /** When a low or exhausted level eases, when the CLI said. */
  resets_at?: string;
  using_overage?: boolean;
  /** Why nothing was measured, in plain words. */
  missing?: string;
  /** When captions and suggestions try the engine again after its rate limit. */
  rate_limited_until?: string;
  /** Prepaid credit left, when the login reports it. */
  credits?: EngineCredits;
}
/** An exact decimal balance, in an ISO currency such as "USD" or in "credits". */
export interface EngineCredits {
  balance: string;
  unit: string;
}
export function getUsage() {
  return api<EngineUsage[]>("/api/usage");
}
export function setPaused(paused: boolean) {
  return api("/api/control", {
    method: "POST",
    body: JSON.stringify({ paused }),
  });
}
export function getConfig() {
  return api<Config>("/api/config");
}
export function putConfig(config: Config) {
  return api("/api/config", { method: "PUT", body: JSON.stringify(config) });
}
/**
 * An engine the daemon can reach, and what it may be used for; the server
 * sends them in the order they are offered.
 */
export interface EngineChoice {
  engine: string;
  label: string;
  /** Reached through a local CLI, with a program and a folder for its login. */
  cli: boolean;
  assistant: boolean;
  roles: boolean;
  /** Why team roles can't run on it, when they can't. */
  roles_reason?: string;
  /** Suggestions and loading lines. */
  small: boolean;
  /** An implementer's conversation on it can be compacted from outside. */
  compact: boolean;
  /** Reports what its subscription has left. */
  usage: boolean;
  models: boolean;
  /** Its model list says which reasoning efforts each model takes. */
  efforts: boolean;
  /** QA on it can use the browser the engine ships. */
  browser: boolean;
}
/** What a blank engine setting falls back to, for showing in its place. */
export interface ConfigDefaults {
  /** An empty home is the CLI's own. */
  engines?: Record<
    string,
    { bin?: string; home?: string; browser_bridge_home?: string }
  >;
  choices?: EngineChoice[];
  usage_floor?: number;
  on_unknown_usage?: "allow" | "pause";
  openai_base_url?: string;
}
export function getConfigDefaults() {
  return api<ConfigDefaults>("/api/config/defaults");
}
export function resolveDecision(
  id: string,
  body: { choice: string } | { answer: string },
) {
  return api(`/api/decisions/${encodeURIComponent(id)}/resolve`, {
    method: "POST",
    body: JSON.stringify(body),
  });
}
export function dismissDecision(id: string, reason: string) {
  return api(`/api/decisions/${encodeURIComponent(id)}/dismiss`, {
    method: "POST",
    body: JSON.stringify({ reason }),
  });
}
export function acknowledgeOperation(id: string, note: string) {
  return api(`/api/operations/${encodeURIComponent(id)}/acknowledge`, {
    method: "POST",
    body: JSON.stringify({ note }),
  });
}
export function setDirectories(projectID: string, paths: string[]) {
  return api<Project>(`${projectPath(projectID)}/directories`, {
    method: "PUT",
    body: JSON.stringify({ directories: paths }),
  });
}

/** Holds new project work without cancelling running steps. */
export function setProjectPaused(projectID: string, paused: boolean) {
  return api<Project>(`${projectPath(projectID)}/paused`, {
    method: "PUT",
    body: JSON.stringify({ paused }),
  });
}

/** Holds, or lets go on, everything landing in a project. */
export function setLandingPaused(
  projectID: string,
  paused: boolean,
  reason = "",
) {
  return api<Project>(`${projectPath(projectID)}/landing-paused`, {
    method: "PUT",
    body: JSON.stringify({ paused, reason }),
  });
}

/** Renames a project; its readable task IDs keep their prefix. */
export function setTitle(projectID: string, title: string) {
  return api<Project>(`${projectPath(projectID)}/title`, {
    method: "PUT",
    body: JSON.stringify({ title }),
  });
}

/** Renames the prefix of a project's readable task IDs. */
export function setPrefix(projectID: string, prefix: string) {
  return api<Project>(`${projectPath(projectID)}/prefix`, {
    method: "PUT",
    body: JSON.stringify({ prefix }),
  });
}

export function createProject(input: ProjectInput) {
  return api<Project>("/api/projects", {
    method: "POST",
    body: JSON.stringify(input),
  });
}
export function updateBrief(projectID: string, input: BriefInput) {
  return api<Project>(`${projectPath(projectID)}/brief`, {
    method: "PUT",
    body: JSON.stringify(input),
  });
}
export function setTeam(projectID: string, input: TeamInput) {
  return api<Project>(`${projectPath(projectID)}/team`, {
    method: "PUT",
    body: JSON.stringify(input),
  });
}
/** Fills one role with a member, or with "" gives it back to the template. */
export function setSeat(projectID: string, kind: MemberKind, member: string) {
  return api<Project>(
    `${projectPath(projectID)}/team/${encodeURIComponent(kind)}`,
    { method: "PUT", body: JSON.stringify({ member }) },
  );
}
/** Adds a seat filled like the named one: Claudius gains Claudius #2. */
export function addSeat(projectID: string, seat: string) {
  return api<Project>(`${projectPath(projectID)}/team/seats`, {
    method: "POST",
    body: JSON.stringify({ seat }),
  });
}
/**
 * Adds someone to a role: a member, or the template's seat when member is
 * empty. A member already in the role gains another seat.
 */
export function addToRole(projectID: string, kind: MemberKind, member: string) {
  return api<Project>(`${projectPath(projectID)}/team/seats`, {
    method: "POST",
    body: JSON.stringify({ kind, member }),
  });
}
/** Takes a seat out of one role; it keeps any other role it holds. */
export function removeFromRole(
  projectID: string,
  seat: string,
  kind: MemberKind,
) {
  return api<Project>(
    `${projectPath(projectID)}/team/seats/${encodeURIComponent(seat)}?kind=${encodeURIComponent(kind)}`,
    { method: "DELETE" },
  );
}
/** Takes the named seat off the team. */
export function removeSeat(projectID: string, seat: string) {
  return api<Project>(
    `${projectPath(projectID)}/team/seats/${encodeURIComponent(seat)}`,
    { method: "DELETE" },
  );
}
/** Sets how many tasks may be under way at once; 0 is one per implementer. */
export function setParallel(projectID: string, maxActive: number) {
  return api<Project>(`${projectPath(projectID)}/parallel`, {
    method: "PUT",
    body: JSON.stringify({ max_active: maxActive }),
  });
}
/** Sets the most requests each working stage may hold; 0 or absent is no limit. */
export function setStageLimits(
  projectID: string,
  limits: Partial<Record<Stage, number>>,
) {
  return api<Project>(`${projectPath(projectID)}/stage-limits`, {
    method: "PUT",
    body: JSON.stringify({ stage_limits: limits }),
  });
}
export function setWorkspace(projectID: string, input: WorkspaceInput) {
  return api<Project>(`${projectPath(projectID)}/workspace`, {
    method: "PUT",
    body: JSON.stringify(input),
  });
}
/** Sets how QA starts the app to use it, or with null takes it away. */
export function setRunRecipe(projectID: string, run: RunRecipe | null) {
  return api<Project>(`${projectPath(projectID)}/run`, {
    method: "PUT",
    body: JSON.stringify({ run }),
  });
}
/** Sets whether this project's QA uses its engine's browser. */
export function setQABrowser(projectID: string, browser: Browser) {
  return api<Project>(`${projectPath(projectID)}/team/qa/browser`, {
    method: "PUT",
    body: JSON.stringify(browser),
  });
}
export function askForTask(projectID: string, input: TaskInput) {
  return api<Task>(`${projectPath(projectID)}/tasks`, {
    method: "POST",
    body: JSON.stringify(input),
  });
}
export function setLanding(projectID: string, input: LandingInput) {
  return api<Project>(`${projectPath(projectID)}/landing`, {
    method: "PUT",
    body: JSON.stringify(input),
  });
}
export function landTask(projectID: string, taskID: string) {
  return api<Task>(
    `${projectPath(projectID)}/tasks/${encodeURIComponent(taskID)}/land`,
    { method: "POST", body: "{}" },
  );
}
export function orderTasks(projectID: string, taskIDs: string[]) {
  return api<Task[]>(`${projectPath(projectID)}/tasks/order`, {
    method: "PUT",
    body: JSON.stringify({ task_ids: taskIDs }),
  });
}
export function messageTeam(
  projectID: string,
  taskID: string,
  to: string,
  text: string,
) {
  return api<TeamMessage>(
    `${projectPath(projectID)}/tasks/${encodeURIComponent(taskID)}/messages`,
    { method: "POST", body: JSON.stringify({ to, text }) },
  );
}
export function linkTasks(
  projectID: string,
  taskID: string,
  relation: Relation,
  other: string,
) {
  return api<Task>(
    `${projectPath(projectID)}/tasks/${encodeURIComponent(taskID)}/links`,
    { method: "POST", body: JSON.stringify({ relation, task: other }) },
  );
}
/** Removes whatever links the two tasks, in either direction. */
export function unlinkTasks(projectID: string, taskID: string, other: string) {
  return api<Task>(
    `${projectPath(projectID)}/tasks/${encodeURIComponent(taskID)}/links/${encodeURIComponent(other)}`,
    { method: "DELETE" },
  );
}
export function addNote(
  projectID: string,
  taskID: string,
  text: string,
  files: File[] = [],
) {
  let body: string | FormData = JSON.stringify({ text });
  if (files.length) {
    // With files, the note and its files are kept together or not at all.
    body = new FormData();
    body.append("text", text);
    for (const file of files) body.append("files", file, file.name);
  }
  return api<Note>(
    `${projectPath(projectID)}/tasks/${encodeURIComponent(taskID)}/notes`,
    { method: "POST", body },
  );
}
/** Where one of a task's attachments is served. */
export function attachmentURL(
  projectID: string,
  taskID: string,
  attachmentID: string,
) {
  return `${projectPath(projectID)}/tasks/${encodeURIComponent(taskID)}/attachments/${encodeURIComponent(attachmentID)}`;
}
/** Puts the title and requirements back as they were before one edit. */
export function undoTaskEdit(
  projectID: string,
  taskID: string,
  editID: string,
) {
  return api<Task>(
    `${projectPath(projectID)}/tasks/${encodeURIComponent(taskID)}/edits/${encodeURIComponent(editID)}/undo`,
    { method: "POST", body: "{}" },
  );
}
export function stopTask(projectID: string, taskID: string) {
  return api<Task>(
    `${projectPath(projectID)}/tasks/${encodeURIComponent(taskID)}/stop`,
    { method: "POST", body: "{}" },
  );
}
/** Moves a request waiting on the owner onto the project's current team. */
export function adoptProjectTeam(projectID: string, taskID: string) {
  return api<Task>(
    `${projectPath(projectID)}/tasks/${encodeURIComponent(taskID)}/team`,
    { method: "POST", body: "{}" },
  );
}
export async function revisionFiles(
  projectID: string,
  taskID: string,
  n: number,
  signal?: AbortSignal,
) {
  const body = await api<{ files: RevisionFile[] | null }>(
    `${projectPath(projectID)}/tasks/${encodeURIComponent(taskID)}/revisions/${n}`,
    { signal },
  );
  return body.files ?? [];
}
/** What one seat has done on a request, oldest first. */
export async function turnSteps(
  projectID: string,
  taskID: string,
  seat: string,
  signal?: AbortSignal,
) {
  const body = await api<{ steps?: TurnStep[] | null }>(
    `${projectPath(projectID)}/tasks/${encodeURIComponent(taskID)}/seats/${encodeURIComponent(seat)}/steps`,
    { signal },
  );
  return body.steps ?? [];
}

const memberPath = (id: string) => `/api/members/${encodeURIComponent(id)}`;

/** saveMember creates a member when id is empty, and changes it otherwise. */
export function saveMember(id: string, input: MemberInput) {
  return api<Member>(id ? memberPath(id) : "/api/members", {
    method: id ? "PUT" : "POST",
    body: JSON.stringify(input),
  });
}
export function deleteMember(id: string) {
  return api<{ deleted: boolean }>(memberPath(id), { method: "DELETE" });
}
/** Draws a member again; an empty look keeps the last one. */
export function redrawMember(id: string, look: string) {
  return api<{ drawing: boolean }>(`${memberPath(id)}/avatar`, {
    method: "POST",
    body: JSON.stringify({ look }),
  });
}
const assistantPath = (id: string) =>
  `/api/assistants/${encodeURIComponent(id)}`;

/** saveAssistant adds an assistant when id is empty, and changes it otherwise. */
export function saveAssistant(id: string, input: AssistantInput) {
  return api<AssistantProfile>(id ? assistantPath(id) : "/api/assistants", {
    method: id ? "PUT" : "POST",
    body: JSON.stringify(input),
  });
}
export function deleteAssistant(id: string) {
  return api<{ deleted: boolean }>(assistantPath(id), { method: "DELETE" });
}
/** Draws an assistant again; an empty look keeps the last one. */
export function redrawAssistant(id: string, look: string) {
  return api<{ drawing: boolean }>(`${assistantPath(id)}/avatar`, {
    method: "POST",
    body: JSON.stringify({ look }),
  });
}
export function addLearning(id: string, input: LearningInput) {
  return api<Member>(`${memberPath(id)}/learnings`, {
    method: "POST",
    body: JSON.stringify(input),
  });
}
export function forgetLearning(id: string, learningId: string) {
  return api<Member>(
    `${memberPath(id)}/learnings/${encodeURIComponent(learningId)}`,
    { method: "DELETE" },
  );
}

let pairingRequest: Promise<void> | null = null;
export function bootstrapSession(): Promise<void> {
  if (pairingRequest) return pairingRequest;
  const token = new URLSearchParams(window.location.hash.slice(1)).get("token");
  if (!token) return Promise.resolve();
  // Remove the one-use credential from the address before any network work.
  window.history.replaceState(
    null,
    "",
    window.location.pathname + window.location.search,
  );
  pairingRequest = api<void>("/api/session", {
    method: "POST",
    body: JSON.stringify({ token }),
  });
  return pairingRequest;
}

export type FileSystemKind = "directory" | "file" | "any";
export interface FileSystemEntry {
  name: string;
  path: string;
  kind: "directory" | "file";
  selectable: boolean;
}
export interface FileSystemPage {
  path: string;
  parent: string | null;
  entries: FileSystemEntry[];
  next_cursor: string | null;
}

export function setBlocker(
  projectID: string,
  taskID: string,
  body: {
    kind: Blocker["kind"];
    description: string;
    task: string;
    landing_only: boolean;
  },
) {
  return api<Task>(
    `${projectPath(projectID)}/tasks/${encodeURIComponent(taskID)}/blockers`,
    { method: "POST", body: JSON.stringify(body) },
  );
}
export function clearBlocker(
  projectID: string,
  taskID: string,
  blockerID: string,
) {
  return api<Task>(
    `${projectPath(projectID)}/tasks/${encodeURIComponent(taskID)}/blockers/${encodeURIComponent(blockerID)}`,
    { method: "DELETE" },
  );
}

export interface PMChatChange {
  kind: "reordered" | "updated" | "linked" | "queued";
  summary: string;
  tasks: string[];
}
export interface PMChatMessage {
  conversation?: string;
  id: string;
  project_id: string;
  from: "owner" | "pm";
  by?: string;
  text: string;
  status: "waiting" | "working" | "answered" | "failed";
  error?: string;
  changes?: PMChatChange[];
  at: string;
  started_at?: string;
  reply_to?: string;
}
const pmChatURL = (project: string) =>
  `/api/projects/${encodeURIComponent(project)}/pm-chat`;
export const getPMChat = (project: string) =>
  api<{ messages: PMChatMessage[]; avatar_svg?: string }>(pmChatURL(project));
export const sendPMChat = (project: string, id: string, text: string) =>
  api<PMChatMessage>(`${pmChatURL(project)}/messages`, {
    method: "POST",
    body: JSON.stringify({ id, text }),
  });
export const retryPMChat = (project: string, id: string) =>
  api<PMChatMessage>(
    `${pmChatURL(project)}/messages/${encodeURIComponent(id)}/retry`,
    { method: "POST" },
  );

export function linkTaskLinear(
  project: string,
  task: string,
  input: {
    connection_id: string;
    profile: string;
    kind: "issue" | "project";
    ref: string;
  },
) {
  return api<Task>(
    `/api/projects/${encodeURIComponent(project)}/tasks/${encodeURIComponent(task)}/linear`,
    { method: "POST", body: JSON.stringify(input) },
  );
}
export function unlinkTaskLinear(
  project: string,
  task: string,
  link: LinearRef,
) {
  return api<Task>(
    `/api/projects/${encodeURIComponent(project)}/tasks/${encodeURIComponent(task)}/linear/${link.kind}/${encodeURIComponent(link.id)}`,
    { method: "DELETE" },
  );
}

export interface BrowserBridgeStatus {
  usable: boolean;
  reason: string;
  home: string;
}
export function getCodexBrowserBridge() {
  return api<BrowserBridgeStatus>("/api/engines/codex/browser-bridge");
}

export function setRelease(projectID: string, release: ReleasePolicy | null) {
  return api<Project>(`/api/projects/${projectID}/release`, {
    method: "PUT",
    body: JSON.stringify(release),
  });
}

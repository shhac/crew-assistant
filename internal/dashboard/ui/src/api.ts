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
  name: string;
  kind: "implementer" | "reviewer" | "qa" | (string & {});
  engine: string;
  model?: string;
  effort?: string;
  instructions?: string;
  /** The member this role was copied from, if any. */
  member?: string;
}
export type MemberKind = "implementer" | "reviewer" | "qa";
export interface Learning {
  id: string;
  text: string;
  project_id?: string;
  at?: string;
}
export interface Member {
  id: string;
  name: string;
  kind: MemberKind;
  engine: string;
  model?: string;
  effort?: string;
  instructions?: string;
  avatar?: AvatarSpec;
  avatar_svg?: string;
  /** Codex is drawing its picture. */
  drawing?: boolean;
  /** Why the last drawing failed. */
  draw_error?: string;
  learnings: Learning[];
  created_at?: string;
}
export interface MemberInput {
  name: string;
  kind: MemberKind;
  engine: string;
  model: string;
  effort: string;
  instructions: string;
}
export interface Playbook {
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
  land?: LandPolicy;
}
function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
/** section reads one group of the loosely typed config, or an empty one. */
export function section(value: unknown): Record<string, unknown> {
  return isRecord(value) ? value : {};
}
export interface LandPolicy {
  means?: string;
  via?: "branch" | "push" | "pull-request" | (string & {});
  target?: string;
  method?: string;
  github?: string;
  approve?: "before" | "none" | (string & {});
}
export interface LandingInput {
  means: string;
  via: string;
  target: string;
  method: string;
  github: string;
  approve: string;
}
export interface Project {
  id: string;
  title: string;
  status: string;
  brief: Brief;
  playbook?: Playbook;
  directories?: string[];
  scratch_directory?: string;
  source_id?: string;
  source_description?: string;
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
  max_rounds: string;
  deliver_to: string;
  repo?: string;
  branch_prefix?: string;
  check?: string;
  prepare?: string[];
  sign?: string;
}
export interface TaskInput {
  objective: string;
  criteria: string[];
}
export type TaskStatus =
  | "queued"
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
  "todo" | "implementing" | "reviewing" | "qa" | "ready" | "done" | "stopped";
export interface Revision {
  n: number;
  brief_version: number;
  files: string[] | null;
  ref?: string;
  clean_merge_of?: number;
  summary?: string;
  at?: string;
}
export interface Finding {
  criterion?: string;
  note: string;
}
export interface Verdict {
  revision: number;
  role: string;
  brief_version: number;
  outcome: "pass" | "revise" | "question" | (string & {});
  summary: string;
  findings?: Finding[];
  question?: string;
  asked?: string;
  at?: string;
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
}
export interface Proposal {
  branch: string;
  number?: number;
  url?: string;
}
export interface Task {
  id: string;
  project_id: string;
  objective: string;
  criteria: string[] | null;
  status: TaskStatus;
  stage: Stage;
  /** The checker at work while the request is being checked. */
  checking?: string;
  detail?: string;
  roles?: Role[];
  playbook?: Playbook;
  max_rounds?: number;
  round: number;
  direction?: string[];
  direction_pending?: number;
  messages?: TeamMessage[];
  proposal?: Proposal;
  branch?: string;
  retry_at?: string;
  revisions: Revision[] | null;
  verdicts: Verdict[] | null;
  decision_id?: string;
  delivered_to?: string;
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
  "choice" | "delivery" | "update" | "question" | "escalation" | "failure";
export interface Decision {
  answer?: string;
  disposition?: "choice" | "custom" | "dismissed";
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
  origin?: "wake" | (string & {});
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
  retry_at?: string;
  revision: number;
  events: ChatToolEvent[];
}
export interface Memory {
  id: string;
  key?: string;
  content: string;
  kind?: string;
  source?: string;
  supersedes?: string;
  superseded_at?: string;
  updated_at?: string;
}
export interface Activity {
  id: string;
  project_id?: string;
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
export interface State {
  pending_operations: PendingOperation[];
  assistant: {
    name: string;
    personality: string;
    theme?: string;
    avatar?: AvatarSpec;
    avatar_svg?: string;
    drawing?: boolean;
    draw_error?: string;
  };
  projects: Project[];
  members: Member[];
  tasks: Task[];
  decisions: Decision[];
  messages: Message[];
  memories: Memory[];
  activity: Activity[];
  integrations: Integration[];
  paused: boolean;
  demo: boolean;
}
export type Config = Record<string, unknown> & {
  assistant?: {
    name?: string;
    personality?: string;
    theme?: string;
    avatar?: AvatarSpec;
    [key: string]: unknown;
  };
  connections?: Connection[];
};
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
  const response = await fetch(path, {
    ...options,
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
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
    assistant: raw.assistant ?? { name: "", personality: "" },
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
    paused: raw.paused ?? false,
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
export function stopTask(projectID: string, taskID: string) {
  return api<Task>(
    `${projectPath(projectID)}/tasks/${encodeURIComponent(taskID)}/stop`,
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

const avatarSizes = [
  { name: "small", px: 48 },
  { name: "medium", px: 128 },
  { name: "large", px: 512 },
] as const;

const avatarPath = (image: string, size: string) =>
  `/api/avatars/${encodeURIComponent(image)}/${size}`;

/**
 * The drawn picture to show at a size on the page, or nothing when there is
 * none. Each size is kept at twice the pixels it is shown at, so it stays
 * sharp on high-density screens.
 */
export function avatarURL(avatar: AvatarSpec | undefined, displayPx: number) {
  if (!avatar?.image) return undefined;
  const size = avatarSizes.find((s) => s.px >= displayPx * 2) ?? avatarSizes[2];
  return avatarPath(avatar.image, size.name);
}

/** Every size of a drawn picture, for the browser to choose from. */
export const avatarSrcSet = (image: string) =>
  avatarSizes.map((s) => `${avatarPath(image, s.name)} ${s.px}w`).join(", ");

/**
 * The small picture as a data URL, for the tab icon: some browsers fetch
 * icons without the session cookie, and the page's policy allows data
 * images.
 */
export async function avatarDataURL(image: string, signal?: AbortSignal) {
  const response = await fetch(avatarPath(image, "small"), {
    credentials: "same-origin",
    signal,
  });
  if (!response.ok) throw new APIError("No such picture", response.status);
  const blob = await response.blob();
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () =>
      typeof reader.result === "string"
        ? resolve(reader.result)
        : reject(new Error("Unreadable picture"));
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(blob);
  });
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
/** Draws the assistant again; an empty look keeps the last one. */
export function redrawAssistant(look: string) {
  return api<{ drawing: boolean }>("/api/assistant/avatar", {
    method: "POST",
    body: JSON.stringify({ look }),
  });
}
export function addLearning(id: string, text: string, projectId: string) {
  return api<Member>(`${memberPath(id)}/learnings`, {
    method: "POST",
    body: JSON.stringify({ text, project_id: projectId }),
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

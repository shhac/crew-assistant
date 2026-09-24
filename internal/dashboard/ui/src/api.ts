import type { Connection } from "./ConnectionsSettings";
import type { AvatarSpec } from "./Identity";
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
  "choice" | "delivery" | "question" | "escalation" | "failure";
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
  };
  projects: Project[];
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
      [message || `Request failed (${response.status})`, body?.hint]
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
    : "Something went wrong. Please try again.";
}
export function criteriaLines(criteria: string[] | string | null): string[] {
  return (Array.isArray(criteria) ? criteria : (criteria || "").split("\n"))
    .map((s) => s.trim())
    .filter(Boolean);
}

const projectPath = (projectID: string) =>
  `/api/projects/${encodeURIComponent(projectID)}`;

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

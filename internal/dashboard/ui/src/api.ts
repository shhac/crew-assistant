import type { Connection } from "./ConnectionsSettings";
import type { AvatarSpec } from "./Identity";
export interface Project {
  id: string;
  title: string;
  description: string;
  acceptance_criteria: string[] | string;
  status: string;
  directories?: string[];
  scratch_directory?: string;
  updated_at?: string;
}
export interface WorkItem {
  id: string;
  project_id: string;
  title: string;
  objective: string;
  acceptance_criteria: string;
  status:
    | "ready"
    | "queued"
    | "waiting"
    | "interrupted"
    | "blocked"
    | "paused"
    | "cancelled"
    | "active"
    | "review"
    | "accepted"
    | "legacy_completed";
  after_work_item_id?: string;
  commission_requested?: boolean;
  status_reason?: string;
  created_at: string;
  updated_at: string;
  review_revision: string;
  acceptance?: {
    revision: string;
    evidence: string[];
    reviewer: string;
    accepted_at: string;
  };
  legacy?: boolean;
}
export interface SteeringMessage {
  id: string;
  work_item_id: string;
  content: string;
  created_at: string;
}
export interface SteeringReceipt {
  message_id: string;
  agent_id: string;
  acknowledged_at: string;
}
/**
 * One observation of what a worker did, as the daemon recorded it. None of it
 * is a claim that the work was correct, and none of it is the evidence
 * acceptance rests on — that is the patch and the command log.
 */
export interface AgentWork {
  at: string;
  kind: string;
  tool?: string;
  status?: string;
  detail?: string;
  truncated?: boolean;
}
export interface Agent {
  retry_at?: string;
  resource_hold_kind?: string;
  resource_hold_owner_action?: boolean;
  resource_hold_resets_at?: string;
  usage_input_tokens?: number;
  usage_output_tokens?: number;
  usage_unknown_calls?: number;
  token_budget?: number;
  provider_failures?: number;
  provider_failure_kind?: string;
  model_failure_engine?: string;
  model_failure_phase?: string;
  model_failure_code?: string;
  model_failure_evidence?: string;
  model_exit_code?: number;
  context_compactions?: number;
  context_used_percent?: number;
  context_quality?: string;
  session_engine?: string;
  session_resumed?: boolean;
  observed_input_tokens?: number;
  observed_output_tokens?: number;
  work?: AgentWork[];
  recoveries?: number;
  work_item_id?: string;
  id: string;
  parent_id?: string;
  name: string;
  role: string;
  status: string;
  project_id: string;
  profile_id?: string;
  task?: string;
  acceptance_criteria?: string;
  broker_updated_at?: string;
  last_progress_at?: string;
  last_update?: string;
  next_check_in?: string;
  summary?: string;
  evidence?: string[];
}
export interface Decision {
  answer?: string;
  disposition?: "choice" | "custom" | "dismissed";
  resolution_reason?: string;
  resolved_at?: string;
  work_item_id?: string;
  id: string;
  agent_id?: string;
  project_id?: string;
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
export interface ProjectAttention {
  project_id: string;
  work_item_id?: string;
  agent_id?: string;
  agent_name?: string;
  execution: string;
  reason?: string;
  next_action: string;
  recovery?: string;
  recovery_at?: string;
  last_progress_at?: string;
  open_decisions: number;
  pending_operations: number;
}
export interface PendingOperation {
  id: string;
  summary: string;
  project_id?: string;
}
export interface ModelProfile {
  engine?: string;
  model?: string;
  effort?: string;
}
export interface WorkerProfile {
  id: string;
  managed?: boolean;
  model_profile?: ModelProfile;
  name?: string;
  endpoint?: string;
  api_key_env?: string;
  capabilities?: string[];
  project_id?: string;
  [key: string]: unknown;
}
export interface State {
  work_items: WorkItem[];
  steering: SteeringMessage[];
  steering_receipts: SteeringReceipt[];
  pending_operations: PendingOperation[];
  assistant: {
    name: string;
    personality: string;
    theme?: string;
    avatar?: AvatarSpec;
  };
  projects: Project[];
  agents: Agent[];
  decisions: Decision[];
  messages: Message[];
  memories: Memory[];
  activity: Activity[];
  attention: ProjectAttention[];
  /** Absolute artifact path to the opaque token that downloads it. */
  artifacts: Record<string, string>;
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
  workers?: WorkerProfile[];
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
    work_items: raw.work_items ?? [],
    steering: raw.steering ?? [],
    steering_receipts: raw.steering_receipts ?? [],
    assistant: raw.assistant ?? { name: "", personality: "" },
    pending_operations: raw.pending_operations ?? [],
    projects: raw.projects ?? [],
    agents: raw.agents ?? [],
    decisions: raw.decisions ?? [],
    messages: raw.messages ?? [],
    memories: raw.memories ?? [],
    activity: raw.activity ?? [],
    attention: raw.attention ?? [],
    artifacts: raw.artifacts ?? {},
    integrations: raw.integrations ?? [],
    paused: raw.paused ?? false,
    demo: raw.demo ?? false,
  };
}
export function pendingDecisions(decisions: Decision[]) {
  return decisions.filter(
    (d) =>
      !["resolved", "answered", "cancelled", "closed", "dismissed"].includes(
        d.status,
      ),
  );
}
export function errorText(error: unknown) {
  return error instanceof Error
    ? error.message
    : "Something went wrong. Please try again.";
}
export function criteriaLines(
  criteria: Project["acceptance_criteria"],
): string[] {
  return (Array.isArray(criteria) ? criteria : (criteria || "").split("\n"))
    .map((s) => s.trim())
    .filter(Boolean);
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

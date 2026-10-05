import { api } from "./api";

export type ToolStatus = "missing" | "installed" | "outdated" | "unknown";
export type ToolAction = "install" | "update" | "skill" | "verify";

export interface ToolSkill {
  name: string;
  status: ToolStatus;
  installed?: string;
  latest?: string;
  /** For the owner's own terminal. */
  command: string;
  runnable: boolean;
  /** What the dashboard runs instead, when it can. */
  run?: string;
}

export interface Tool {
  id: string;
  name: string;
  purpose: string;
  formula: string;
  status: ToolStatus;
  installed?: string;
  latest?: string;
  path?: string;
  detail?: string;
  install: string;
  update: string;
  skill?: ToolSkill;
  setup: string[] | null;
  verify: boolean;
  verify_command?: string;
  connection: boolean;
}

export interface ToolJob {
  id: string;
  tool?: string;
  action: ToolAction | "update-all";
  command: string;
  state: "running" | "succeeded" | "failed";
  result?: string;
  started_at: string;
  finished_at?: string;
  lines: string[] | null;
  next: number;
  skipped?: number;
}

export interface Toolkit {
  homebrew: { available: boolean; prefix?: string; install?: string };
  npx: boolean;
  tools: Tool[];
  checked_at?: string;
  job?: ToolJob;
}

export function getToolkit(refresh = false) {
  return api<Toolkit>(`/api/toolkit${refresh ? "?refresh=true" : ""}`);
}

export function startToolJob(tool: string, action: ToolAction) {
  return api<ToolJob>(
    `/api/toolkit/${encodeURIComponent(tool)}/${encodeURIComponent(action)}`,
    { method: "POST" },
  );
}

export function updateAllTools() {
  return api<ToolJob>("/api/toolkit/update-all", { method: "POST" });
}

export function getToolJob(id: string, after: number) {
  return api<ToolJob>(
    `/api/toolkit/jobs/${encodeURIComponent(id)}?after=${after}`,
  );
}

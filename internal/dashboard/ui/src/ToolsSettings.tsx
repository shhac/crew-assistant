import { useEffect, useRef, useState } from "react";
import { Panel } from "./SettingsPanel";
import { errorText } from "./api";
import {
  CopyCommand,
  ErrorNotice,
  Pill,
  sinceLabel,
  useAction,
  useNewestInView,
} from "./ui";
import {
  getToolJob,
  getToolkit,
  startToolJob,
  updateAllTools,
  type Tool,
  type ToolAction,
  type ToolJob,
  type ToolStatus,
  type Toolkit,
} from "./toolkit";

/**
 * An action waiting for the owner to confirm the exact command: key is the
 * tool it belongs to, or "update-all".
 */
interface Pending {
  key: string;
  command: string;
  start: () => Promise<ToolJob>;
}

const statusLabels: Record<ToolStatus, { label: string; tone: string }> = {
  missing: { label: "Not installed", tone: "wait" },
  installed: { label: "Installed", tone: "done" },
  outdated: { label: "Update available", tone: "needs" },
  unknown: { label: "Unknown", tone: "" },
};

/**
 * The shhac CLI family on this computer: install or update each tool with
 * Homebrew, add the skill that teaches agents to use it, and check its
 * sign-in. Signing in itself happens in the owner's terminal.
 */
export function ToolsSettings({ pollMs = 1000 }: { pollMs?: number }) {
  const [toolkit, setToolkit] = useState<Toolkit | null>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  const loading = useAction();
  const starting = useAction();
  async function load(refresh: boolean) {
    const next = await getToolkit(refresh);
    setToolkit(next);
    return next;
  }
  const following = useFollowedJob(pollMs, () => load(false));
  const { follow } = following;
  useEffect(() => {
    void loading.run(async () => {
      const first = await load(false);
      // A job still running from before this page opened is picked up,
      // log and all.
      if (first.job?.state === "running") follow(first.job, 0);
    });
  }, [follow]);
  async function confirm() {
    if (!pending) return;
    const chosen = pending;
    await starting.run(async () => {
      const started = await chosen.start();
      setPending(null);
      follow(started, started.next);
    });
  }
  if (!toolkit)
    return (
      <Panel title="Tools">
        <ErrorNotice error={loading.error} />
        {!loading.error && <p className="muted">Looking for tools…</p>}
      </Panel>
    );
  const { job, running } = following;
  const busy = running || starting.busy;
  const ask = (next: Pending) => {
    starting.setError("");
    setPending(next);
  };
  const cancel = () => setPending(null);
  const updateAll = toolkit.update_all;
  return (
    <Panel title="Tools">
      <p className="soft">
        Command-line tools your assistant and team can use, with the skills that
        teach agents to use them. Installing uses Homebrew; signing in happens
        in your terminal.
      </p>
      {!toolkit.homebrew.available && (
        <div className="banner banner-alert" role="status">
          <p>
            Homebrew isn't installed, so tools can't be installed from here.
            Install it from <a href="https://brew.sh">brew.sh</a> by running
            this in your terminal, then check again:
          </p>
          {toolkit.homebrew.install && (
            <CopyCommand command={toolkit.homebrew.install} />
          )}
        </div>
      )}
      <div className="actions">
        <button
          type="button"
          className="btn btn-sm"
          disabled={loading.busy}
          onClick={() => void loading.run(() => load(true))}
        >
          {loading.busy ? "Checking…" : "Check for updates"}
        </button>
        {updateAll && (
          <button
            type="button"
            className="btn btn-sm"
            disabled={busy}
            onClick={() =>
              ask({
                key: "update-all",
                command: updateAll,
                start: () => updateAllTools(updateAll),
              })
            }
          >
            Update all
          </button>
        )}
        {toolkit.checked_at && (
          <span className="muted small">
            Latest versions checked {sinceLabel(toolkit.checked_at)}
          </span>
        )}
      </div>
      {pending?.key === "update-all" && (
        <Confirm
          pending={pending}
          busy={starting.busy}
          onConfirm={() => void confirm()}
          onCancel={cancel}
        />
      )}
      <ErrorNotice error={loading.error || starting.error || following.error} />
      {job && (
        <JobLog
          job={job}
          onClose={running ? undefined : () => following.close()}
        />
      )}
      <ul className="rows toolkit-rows">
        {toolkit.tools.map((tool) => (
          <ToolRow
            key={tool.id}
            tool={tool}
            homebrew={toolkit.homebrew.available}
            busy={busy}
            pending={pending?.key === tool.id ? pending : null}
            confirming={starting.busy}
            onAsk={ask}
            onConfirm={() => void confirm()}
            onCancel={cancel}
          />
        ))}
      </ul>
    </Panel>
  );
}

/**
 * Follows one job's log by polling from the last line read, until it ends;
 * then onFinished runs once. A failed poll is shown and retried, and the
 * error clears once a poll succeeds.
 */
function useFollowedJob(pollMs: number, onFinished: () => Promise<unknown>) {
  const [job, setJob] = useState<ToolJob | null>(null);
  const [error, setError] = useState("");
  // The log line the next poll reads from.
  const cursor = useRef(0);
  const finished = useRef(onFinished);
  finished.current = onFinished;
  const follow = useRef((next: ToolJob, from: number) => {
    cursor.current = from;
    setError("");
    setJob({ ...next, lines: next.lines ?? [] });
  }).current;
  const jobID = job?.id;
  const running = job?.state === "running";
  useEffect(() => {
    if (!jobID || !running) return;
    const control: {
      stopped: boolean;
      timer?: ReturnType<typeof setTimeout>;
    } = { stopped: false };
    const schedule = (delay: number) => {
      control.timer = setTimeout(() => void poll(jobID), delay);
    };
    async function poll(id: string) {
      try {
        const next = await getToolJob(id, cursor.current);
        if (control.stopped) return;
        cursor.current = next.next;
        setError("");
        setJob((current) => ({
          ...next,
          lines: [...(current?.lines ?? []), ...(next.lines ?? [])],
        }));
        if (next.state === "running") return schedule(pollMs);
        await finished.current().catch(() => {});
      } catch (err) {
        if (control.stopped) return;
        setError(errorText(err));
        schedule(pollMs * 3);
      }
    }
    schedule(pollMs);
    return () => {
      control.stopped = true;
      clearTimeout(control.timer);
    };
  }, [jobID, running, pollMs]);
  return { job, running, error, follow, close: () => setJob(null) };
}

function ToolRow({
  tool,
  homebrew,
  busy,
  pending,
  confirming,
  onAsk,
  onConfirm,
  onCancel,
}: {
  tool: Tool;
  homebrew: boolean;
  busy: boolean;
  pending: Pending | null;
  confirming: boolean;
  onAsk: (pending: Pending) => void;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const status = statusLabels[tool.status] ?? statusLabels.unknown;
  const installed = tool.status !== "missing";
  const skill = tool.skill;
  const skillWanted =
    !!skill && (skill.status === "missing" || skill.status === "outdated");
  const skillRun = skillWanted && skill.runnable ? skill.run : undefined;
  const verifyCommand =
    installed && tool.verify ? tool.verify_command : undefined;
  const setup = tool.setup ?? [];
  const ask = (action: ToolAction, command: string) =>
    onAsk({
      key: tool.id,
      command,
      start: () => startToolJob(tool.id, action),
    });
  return (
    <li className="toolkit-row">
      <div className="panel-head">
        <span>
          <strong>{tool.name}</strong> <code className="small">{tool.id}</code>
        </span>
        <Pill tone={status.tone}>{status.label}</Pill>
      </div>
      <p className="soft small">{tool.purpose}</p>
      <p className="muted small">{versionText(tool)}</p>
      {tool.detail && <p className="muted small">{tool.detail}</p>}
      {skill && <p className="muted small">Skill: {skillText(skill)}</p>}
      <div className="actions">
        {tool.status === "missing" && (
          <button
            type="button"
            className="btn btn-sm btn-primary"
            disabled={busy || !homebrew}
            onClick={() => ask("install", tool.install)}
          >
            Install
          </button>
        )}
        {tool.status === "outdated" && (
          <button
            type="button"
            className="btn btn-sm btn-primary"
            disabled={busy || !homebrew}
            onClick={() => ask("update", tool.update)}
          >
            Update
          </button>
        )}
        {skillRun && (
          <button
            type="button"
            className="btn btn-sm"
            disabled={busy}
            onClick={() => ask("skill", skillRun)}
          >
            {skill?.status === "outdated" ? "Update skill" : "Install skill"}
          </button>
        )}
        {verifyCommand && (
          <button
            type="button"
            className="btn btn-sm btn-quiet"
            disabled={busy}
            onClick={() => ask("verify", verifyCommand)}
          >
            Check sign-in
          </button>
        )}
      </div>
      {pending && (
        <Confirm
          pending={pending}
          busy={confirming}
          onConfirm={onConfirm}
          onCancel={onCancel}
        />
      )}
      {skillWanted && !skill.runnable && (
        <div className="toolkit-hint">
          <p className="muted small">
            To add its skill, run this in your terminal:
          </p>
          <CopyCommand command={skill.command} />
        </div>
      )}
      {setup.length > 0 && (
        <details className="disclosure">
          <summary>Sign in</summary>
          <div className="disclosure-body toolkit-hint">
            <p className="muted small">
              {installed ? "Run" : "Once it's installed, run"} in your terminal:
            </p>
            {setup.map((line) => (
              <CopyCommand key={line} command={line} />
            ))}
          </div>
        </details>
      )}
    </li>
  );
}

function versionText(tool: Tool) {
  if (tool.status === "missing")
    return tool.latest ? `Latest is ${tool.latest}` : "Latest version unknown";
  if (!tool.installed)
    return tool.latest ? `Latest is ${tool.latest}` : "Version unknown";
  if (tool.status === "outdated")
    return `${tool.installed} installed · ${tool.latest} available`;
  return tool.latest
    ? `${tool.installed} installed, the latest`
    : `${tool.installed} installed · latest unknown`;
}

function skillText(skill: NonNullable<Tool["skill"]>) {
  switch (skill.status) {
    case "missing":
      return "not installed";
    case "outdated":
      return `newer version${skill.latest ? ` ${skill.latest}` : ""} available`;
    case "installed":
      return skill.installed ? `installed ${skill.installed}` : "installed";
  }
  return "status unknown";
}

function Confirm({
  pending,
  busy,
  onConfirm,
  onCancel,
}: {
  pending: Pending;
  busy: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <div className="toolkit-confirm" role="group" aria-label="Confirm command">
      <p className="small">This runs on this computer:</p>
      <pre className="step-pre">{pending.command}</pre>
      <div className="actions">
        <button
          type="button"
          className="btn btn-sm btn-primary"
          disabled={busy}
          onClick={onConfirm}
        >
          Run
        </button>
        <button
          type="button"
          className="btn btn-sm btn-quiet"
          disabled={busy}
          onClick={onCancel}
        >
          Cancel
        </button>
      </div>
    </div>
  );
}

const jobLabels: Record<ToolJob["state"], { label: string; tone: string }> = {
  running: { label: "Running", tone: "work" },
  succeeded: { label: "Done", tone: "done" },
  failed: { label: "Failed", tone: "block" },
};

function JobLog({ job, onClose }: { job: ToolJob; onClose?: () => void }) {
  const box = useNewestInView<HTMLPreElement>("bottom");
  const lines = job.lines ?? [];
  const state = jobLabels[job.state] ?? jobLabels.running;
  return (
    <section className="toolkit-job" aria-label="Command output">
      <div className="panel-head">
        <code className="small">{job.command}</code>
        <span className="actions">
          <Pill tone={state.tone} dot={job.state === "running"}>
            {state.label}
          </Pill>
          {onClose && (
            <button
              type="button"
              className="btn btn-quiet btn-sm"
              onClick={onClose}
            >
              Close
            </button>
          )}
        </span>
      </div>
      <pre className="step-pre" {...box}>
        {lines.length ? lines.join("\n") : "Waiting for output…"}
      </pre>
      {job.result && <p className="small">{job.result}</p>}
    </section>
  );
}

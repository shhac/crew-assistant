// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { Settings } from "./SettingsPage";
import { Sidebar } from "./Sidebar";
import { DecisionCard } from "./DecisionCard";
import { InboxPage } from "./InboxPage";
import { normalizeState, type Config, type Decision } from "./api";

vi.mock("./UsageStatus", () => ({ UsageStatus: () => null }));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const update = {
  running: "v1.0.0",
  available: "v1.1.0",
  mode: "ask" as const,
  notes: "New features",
};
function sidebar(skipped = "", decisions: Decision[] = []) {
  render(
    <Sidebar
      state={normalizeState({
        update: { ...update, skipped, decision_version: update.available },
        decisions,
      })}
      route={{ page: "inbox" }}
      needs={0}
      offline={false}
      chatOpen={false}
      onChat={() => {}}
      pausing={false}
      pauseError=""
      onPause={() => {}}
    />,
  );
}

it("shows running and available versions, with a link to Updates", () => {
  sidebar();
  expect(screen.getByText("v1.0.0")).toBeTruthy();
  expect(
    screen.getByRole("link", { name: "v1.1.0 available" }).getAttribute("href"),
  ).toBe("#/settings/updates");
});
it("hides a skipped version but keeps the running version", () => {
  sidebar("v1.1.0");
  expect(screen.getByText("v1.0.0")).toBeTruthy();
  expect(screen.queryByText("v1.1.0 available")).toBeNull();
});
it("links to the open update decision and falls back to Settings once it closes", () => {
  const decision: Decision = {
    id: "notice /1",
    kind: "upgrade-available",
    title: "v1.1.0 available",
    context: "Notes",
    recommendation: "By hand",
    choices: ["I'll upgrade by hand", "Skip v1.1.0"],
    status: "open",
  };
  sidebar("", [decision]);
  expect(
    screen.getByRole("link", { name: "v1.1.0 available" }).getAttribute("href"),
  ).toBe("#/inbox/notice%20%2F1");
  cleanup();
  sidebar("", [{ ...decision, status: "resolved" }]);
  expect(
    screen.getByRole("link", { name: "v1.1.0 available" }).getAttribute("href"),
  ).toBe("#/settings/updates");
});
it("focuses the addressed decision and explains automatic closures in the inbox", () => {
  const open: Decision = {
    id: "notice",
    kind: "upgrade-available",
    title: "v1.1.0 available",
    context: "Notes",
    recommendation: "By hand",
    choices: ["I'll upgrade by hand", "Skip v1.1.0"],
    status: "open",
  };
  const done: Decision = {
    ...open,
    id: "done",
    status: "resolved",
    disposition: "completed",
    resolution_reason: "The running version includes this update.",
  };
  const older: Decision = {
    ...open,
    id: "older",
    status: "resolved",
    disposition: "superseded",
    resolution_reason: "Superseded by v1.1.0.",
  };
  render(
    <InboxPage
      state={normalizeState({ decisions: [open, done, older] })}
      decision="notice"
      refresh={async () => {}}
      onNew={() => {}}
    />,
  );
  expect(document.activeElement?.id).toBe("decision-notice");
  fireEvent.click(screen.getByText("Past decisions (2)"));
  expect(screen.getByText(done.resolution_reason!)).toBeTruthy();
  expect(screen.getByText(older.resolution_reason!)).toBeTruthy();
  expect(screen.queryByText(/You chose:/)).toBeNull();
});
it("saves all three update modes through the existing config save", async () => {
  const saved: Config[] = [];
  const cfg: Config = {
    upgrade: { mode: "ask", formula: "fixture/tap/application" },
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, opts?: RequestInit) => {
      if (url === "/api/config" && opts?.method === "PUT")
        saved.push(JSON.parse(String(opts.body)));
      return { ok: true, json: async () => (url === "/api/config" ? cfg : {}) };
    }),
  );
  render(
    <Settings
      state={normalizeState({
        update: {
          ...update,
          checked_at: "2026-01-01T00:00:00Z",
          error: "HTTP 429",
        },
      })}
      refresh={async () => {}}
      section="updates"
    />,
  );
  const select = await screen.findByRole("combobox", {
    name: "Update notices",
  });
  expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
    "Off",
    "Ask me",
    "Automatic",
  ]);
  expect(screen.getByText(/Last checked/)).toBeTruthy();
  expect(screen.getByText("HTTP 429")).toBeTruthy();
  fireEvent.change(select, { target: { value: "off" } });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(saved[0]?.upgrade?.mode).toBe("off"));
  await waitFor(() =>
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull(),
  );
  fireEvent.change(select, { target: { value: "ask" } });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(saved[1]?.upgrade?.mode).toBe("ask"));
  await waitFor(() =>
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull(),
  );
  fireEvent.change(select, { target: { value: "automatic" } });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(saved[2]?.upgrade?.mode).toBe("automatic"));
  expect(
    saved.every(
      (value) => value.upgrade?.formula === "fixture/tap/application",
    ),
  ).toBe(true);
  expect(screen.getByText(/quiet moment.*6-hour cap/)).toBeTruthy();
  expect(screen.getByText(/upgrade choice for Homebrew installs/)).toBeTruthy();
  expect(saved[1].upgrade?.formula).toBe("fixture/tap/application");
});
it("renders the project-less upgrade decision with manual choices", () => {
  const decision: Decision = {
    id: "update",
    kind: "upgrade-available",
    title: "v1.1.0 is available",
    context:
      "Running v1.0.0; v1.1.0 is available.\nNew features\nbrew upgrade fixture/tap/application",
    recommendation: "Upgrade by hand when ready.",
    status: "open",
    choices: ["I'll upgrade by hand", "Skip v1.1.0"],
  };
  render(<DecisionCard decision={decision} refresh={async () => {}} full />);
  expect(screen.getByText(/Running v1.0.0/)).toBeTruthy();
  expect(
    screen.getByRole("button", { name: "I'll upgrade by hand" }),
  ).toBeTruthy();
  expect(screen.getByRole("button", { name: "Skip v1.1.0" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: /^Upgrade to/ })).toBeNull();
});

it("shows the rollback pin in Updates while Automatic is selected", async () => {
  const config: Config = {
    upgrade: { mode: "automatic", formula: "fixture/tap/app" },
    assistant: { seat: "fixture" },
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({ ok: true, json: async () => config })),
  );
  const rollback = {
    from: "v1.0.0",
    to: "v1.1.0",
    failure: "health failed <img src=x>",
    clear: "crew-assistant upgrade clear-rollback",
  };
  const view = render(
    <Settings
      state={normalizeState({ rollback })}
      refresh={async () => {}}
      section="updates"
    />,
  );
  const select = await screen.findByRole("combobox", {
    name: "Update notices",
  });
  expect((select as HTMLSelectElement).value).toBe("automatic");
  expect(
    screen.getByText(/Rollback is in force.*health failed <img src=x>/),
  ).toBeTruthy();
  expect(view.container.querySelector("img[src=x]")).toBeNull();
  expect(screen.getByText(rollback.clear)).toBeTruthy();
  expect(
    screen.getByText(/fresh backups.*custom state\/config flags/),
  ).toBeTruthy();
  view.rerender(
    <Settings
      state={normalizeState({})}
      refresh={async () => {}}
      section="updates"
    />,
  );
  expect(screen.queryByText(/Rollback is in force/)).toBeNull();
});

it("keeps the Automatic draft after a refused save and preserves unrelated config on retry", async () => {
  const config: Config = {
    upgrade: { mode: "ask", formula: "fixture/tap/app" },
    assistant: { seat: "fixture" },
  };
  let refuse = true;
  const saved: Config[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, options?: RequestInit) => {
      if (url === "/api/config" && options?.method === "PUT") {
        saved.push(JSON.parse(String(options.body)));
        if (refuse)
          return {
            ok: false,
            status: 503,
            json: async () => ({ error: "upgrading" }),
          };
      }
      return {
        ok: true,
        json: async () => (url === "/api/config" ? config : {}),
      };
    }),
  );
  render(
    <Settings
      state={normalizeState({})}
      refresh={async () => {}}
      section="updates"
    />,
  );
  const select = await screen.findByRole("combobox", {
    name: "Update notices",
  });
  fireEvent.change(select, { target: { value: "automatic" } });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(await screen.findByText("upgrading")).toBeTruthy();
  expect((select as HTMLSelectElement).value).toBe("automatic");
  expect(config.upgrade?.mode).toBe("ask");
  refuse = false;
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(saved).toHaveLength(2));
  expect(saved[1]).toMatchObject({
    upgrade: { mode: "automatic", formula: "fixture/tap/app" },
    assistant: { seat: "fixture" },
  });
});

// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { QABrowser } from "./QABrowser";
import { RunRecipeSettings } from "./RunRecipe";
import { VerdictEvidence } from "./VerdictEvidence";
import { rememberChoices } from "./engines";
import { testChoices } from "./testEngines";
import type { Playbook, Project, Task } from "./api";

rememberChoices(testChoices);

let calls: { path: string; method?: string; body: unknown }[];
beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, options?: RequestInit) => {
      calls.push({
        path,
        method: options?.method,
        body: JSON.parse(String(options?.body ?? "null")),
      });
      return { ok: true, status: 200, json: async () => ({}) };
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const playbook = (qaEngine: string, extra: Partial<Playbook> = {}) =>
  ({
    template: "code",
    medium: "git",
    max_rounds: 3,
    deliver: "owner",
    roles: [
      { name: "Implementer", kinds: ["implementer"], engine: "claude" },
      { name: "Quinn", kinds: ["qa"], engine: qaEngine, member: "m1" },
    ],
    ...extra,
  }) as Playbook;
const project = (p: Playbook) => ({ id: "p1", playbook: p }) as Project;

it("adds a run recipe, explaining that setup runs offline", async () => {
  const refresh = vi.fn(async () => {});
  const p = playbook("claude");
  render(
    <RunRecipeSettings project={project(p)} playbook={p} refresh={refresh} />,
  );
  expect(screen.getByText(/No recipe, so QA runs the check only/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Add a recipe" }));
  expect(screen.getByText(/setup runs offline/)).toBeTruthy();
  fireEvent.change(screen.getByLabelText(/^Start/), {
    target: { value: " npm start " },
  });
  fireEvent.change(screen.getByLabelText(/^Address/), {
    target: { value: "http://127.0.0.1:{port}/" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(refresh).toHaveBeenCalled());
  expect(calls).toEqual([
    {
      path: "/api/projects/p1/run",
      method: "PUT",
      body: {
        run: {
          setup: "",
          start: "npm start",
          url: "http://127.0.0.1:{port}/",
          ready: "",
        },
      },
    },
  ]);
});

it("shows a recipe and takes it away", async () => {
  const refresh = vi.fn(async () => {});
  const p = playbook("claude", {
    run: { start: "npm start", url: "http://localhost:{port}/" },
  });
  render(
    <RunRecipeSettings project={project(p)} playbook={p} refresh={refresh} />,
  );
  expect(screen.getByText("npm start")).toBeTruthy();
  expect(screen.getByText("The address answers")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  fireEvent.click(screen.getByRole("button", { name: "Remove recipe" }));
  await waitFor(() => expect(refresh).toHaveBeenCalled());
  expect(calls[0]).toEqual({
    path: "/api/projects/p1/run",
    method: "PUT",
    body: { run: null },
  });
});

it("lets the owner turn QA's browser on only where its engine has one", async () => {
  const refresh = vi.fn(async () => {});
  const claude = playbook("claude");
  render(
    <QABrowser project={project(claude)} playbook={claude} refresh={refresh} />,
  );
  expect(screen.getByText(/Off. QA doesn't use a browser/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  fireEvent.click(
    screen.getByRole("checkbox", {
      name: "QA uses the browser to try the app",
    }),
  );
  expect(
    screen.getByText("This is your real Chrome, with its logins."),
  ).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(refresh).toHaveBeenCalled());
  expect(calls[0]).toEqual({
    path: "/api/projects/p1/team/qa/browser",
    method: "PUT",
    body: { on: true, name: "" },
  });
  cleanup();
  const codex = playbook("codex");
  render(
    <QABrowser project={project(codex)} playbook={codex} refresh={refresh} />,
  );
  expect(screen.queryByLabelText("QA's browser")).toBeNull();
});

it("shows what QA saw under its verdict, screenshots from the task's files", () => {
  const task = {
    id: "t1",
    project_id: "p1",
    attachments: [
      {
        id: "a1",
        name: "screenshot-1.png",
        type: "image/png",
        size: 2048,
        by: "Quinn",
        kind: "qa",
        at: "",
        verdict: "v1",
      },
    ],
  } as Task;
  render(
    <VerdictEvidence
      task={task}
      verdict={{
        id: "v1",
        revision: 1,
        role: "Quinn",
        brief_version: 1,
        outcome: "pass",
        summary: "It works.",
        evidence: [
          { kind: "screenshot", attachment: "a1" },
          { kind: "console", text: "No errors." },
          { kind: "screenshot", text: "2 more screenshots were not kept." },
        ],
      }}
    />,
  );
  const saw = screen.getByLabelText("What Quinn saw");
  const shot = within(saw).getByRole("img", { name: "screenshot-1.png" });
  expect(shot.getAttribute("src")).toBe(
    "/api/projects/p1/tasks/t1/attachments/a1",
  );
  expect(within(saw).getByText("No errors.")).toBeTruthy();
  expect(
    within(saw).getByText("2 more screenshots were not kept."),
  ).toBeTruthy();
});

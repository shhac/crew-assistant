// @vitest-environment jsdom
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { TeamSettings } from "./TeamSettings";
import type { Project } from "./api";
import { recordFetch, reply } from "./testFetch";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const catalog = [
  {
    name: "sprite-atlas",
    description: "Designer animation guidance",
    role: "designer",
  },
  {
    name: "sprite-atlas-pipeline",
    description: "Deterministic integration guidance",
    role: "implementer",
  },
];
function project(disabled: string[] = []) {
  return {
    id: "p1",
    title: "Animation",
    playbook: {
      template: "draft",
      medium: "documents",
      roles: [],
      max_rounds: 3,
      deliver: "owner",
      max_active: 2,
      disabled_bundled_skills: disabled,
    },
  } as unknown as Project;
}

it("labels bundled skills by role and saves independent toggles with refresh persistence", async () => {
  let current = project();
  const { calls } = recordFetch((path, options) => {
    if (path === "/api/bundled-skills") return reply(catalog);
    const name = path.split("/").at(-1)!;
    const enabled = JSON.parse(String(options?.body)).enabled;
    const disabled = current.playbook!.disabled_bundled_skills!.filter(
      (item) => item !== name,
    );
    if (!enabled) disabled.push(name);
    current = project(disabled);
    return reply(current);
  });
  const refresh = vi.fn(async () => {
    view.rerender(
      <TeamSettings project={current} members={[]} refresh={refresh} />,
    );
  });
  const view = render(
    <TeamSettings project={current} members={[]} refresh={refresh} />,
  );
  const designer = await screen.findByRole<HTMLInputElement>("checkbox", {
    name: /sprite-atlas \(designer\)/,
  });
  const implementer = screen.getByRole<HTMLInputElement>("checkbox", {
    name: /sprite-atlas-pipeline \(implementer\)/,
  });
  expect(designer.checked).toBe(true);
  expect(implementer.checked).toBe(true);
  expect(
    screen.getByText(/existing requests keep their settings/),
  ).toBeTruthy();
  fireEvent.click(designer);
  await waitFor(() => expect(designer.checked).toBe(false));
  expect(implementer.checked).toBe(true);
  expect(calls[1]).toEqual({
    path: "/api/projects/p1/bundled-skills/sprite-atlas",
    options: expect.objectContaining({
      method: "PUT",
      body: JSON.stringify({ enabled: false }),
    }),
  });
  expect(current.playbook!.max_active).toBe(2);
  fireEvent.click(implementer);
  await waitFor(() => expect(implementer.checked).toBe(false));
  fireEvent.click(designer);
  await waitFor(() => expect(designer.checked).toBe(true));
  expect(implementer.checked).toBe(false);
  view.unmount();
  render(<TeamSettings project={current} members={[]} refresh={refresh} />);
  expect(
    (
      await screen.findByRole<HTMLInputElement>("checkbox", {
        name: /sprite-atlas-pipeline \(implementer\)/,
      })
    ).checked,
  ).toBe(false);
});

it("preserves settings and shows a failed save", async () => {
  recordFetch((path) =>
    path === "/api/bundled-skills"
      ? reply(catalog)
      : reply({ error: "Save refused" }, 500),
  );
  const refresh = vi.fn(async () => {});
  render(<TeamSettings project={project()} members={[]} refresh={refresh} />);
  const checkbox = await screen.findByRole<HTMLInputElement>("checkbox", {
    name: /sprite-atlas \(designer\)/,
  });
  fireEvent.click(checkbox);
  await screen.findByText("Save refused");
  expect(checkbox.checked).toBe(true);
  expect(refresh).not.toHaveBeenCalled();
});

it("reports a catalog read failure", async () => {
  recordFetch(() => reply({ error: "Catalog unavailable" }, 500));
  render(
    <TeamSettings project={project()} members={[]} refresh={async () => {}} />,
  );
  await screen.findByText("Catalog unavailable");
  expect(screen.queryByRole("checkbox")).toBeNull();
});

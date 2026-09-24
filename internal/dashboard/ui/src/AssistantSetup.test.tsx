// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { AssistantSetup } from "./AssistantSetup";
import { ConnectionsSettings } from "./ConnectionsSettings";
import { useState } from "react";
import type { Connection } from "./ConnectionsSettings";

const recommendation = {
  id: "proposal-1",
  name: "Rowan",
  personality: "Calm, direct, and thoughtful.",
  avatar: { shape: "leaf", background: "#202424", accent: "#aacbbb" },
  rationale: "A quiet style to match your preferences.",
};
let requests: { path: string; options?: RequestInit }[];
let response: (path: string) => unknown;
beforeEach(() => {
  requests = [];
  response = () => ({ messages: [], questions: [], recommendation });
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, options?: RequestInit) => {
      requests.push({ path, options });
      return { ok: true, status: 200, json: async () => response(path) };
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("keeps a proposed identity unchanged until the owner applies it", async () => {
  const applied = vi.fn(async () => {});
  response = (path) =>
    path.endsWith("/apply")
      ? recommendation
      : { messages: [], questions: [], recommendation };
  render(
    <AssistantSetup currentName="Iris" demo={false} onApplied={applied} />,
  );
  const button = await screen.findByRole("button", { name: "Use this" });
  expect(
    screen.getByRole("heading", {
      level: 2,
      name: "Suggest a name and personality",
    }),
  ).toBeTruthy();
  expect(
    screen.getByText(
      "Answer a question or two, and the assistant suggests a name, a personality and an avatar.",
    ),
  ).toBeTruthy();
  expect(
    screen.getAllByText(/Nothing changes until you use it\./),
  ).toHaveLength(1);
  expect(screen.getByRole("heading", { level: 3, name: "Rowan" })).toBeTruthy();
  expect(screen.getByText("Calm, direct, and thoughtful.")).toBeTruthy();
  expect(
    screen.getByText("A quiet style to match your preferences."),
  ).toBeTruthy();
  expect(screen.queryByText(/graphite|theme/i)).toBeNull();
  expect(screen.getByRole("status").textContent).toBe(
    "Nothing changes until you use it.",
  );
  expect(applied).not.toHaveBeenCalled();
  expect(requests.some((r) => r.path.endsWith("/apply"))).toBe(false);
  fireEvent.click(button);
  expect(await screen.findByRole("button", { name: "In use" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(screen.getByRole("status").textContent).toBe("Saved.");
  expect(applied).toHaveBeenCalledTimes(1);
  expect(
    JSON.parse(
      requests.find((r) => r.path.endsWith("/apply"))!.options!.body as string,
    ),
  ).toEqual({ recommendation_id: "proposal-1", accepted: true });
});
it("restores a previously applied recommendation without offering to apply it again", async () => {
  response = () => ({
    messages: [],
    questions: [],
    recommendation: { ...recommendation, applied: true },
  });
  render(
    <AssistantSetup currentName="Rowan" demo={false} onApplied={vi.fn()} />,
  );
  expect(await screen.findByRole("button", { name: "In use" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(screen.queryByText("Nothing changes until you use it.")).toBeNull();
  expect(screen.getByText("Saved.")).toBeTruthy();
});
it("keeps named account bindings distinct and enables Notion with its CLI default", async () => {
  response = (path) =>
    path.includes("agent-notion")
      ? {
          tool: "agent-notion",
          available: true,
          selectable: false,
          profiles: [],
          detail: "Uses the CLI default account",
        }
      : {
          tool: "agent-slack",
          available: true,
          selectable: true,
          profiles: [{ name: "work" }, { name: "personal" }],
        };
  let latest: Connection[] = [];
  function Harness() {
    const [connections, setConnections] = useState<Connection[]>([
      {
        id: "work-slack",
        name: "Work conversations",
        tool: "agent-slack",
        profiles: ["work"],
      },
      {
        id: "personal-slack",
        name: "Personal conversations",
        tool: "agent-slack",
        profiles: ["personal"],
      },
    ]);
    latest = connections;
    return (
      <ConnectionsSettings
        connections={connections}
        onChange={setConnections}
      />
    );
  }
  render(<Harness />);
  await waitFor(() =>
    expect(screen.getAllByLabelText("work")[0]).toHaveProperty(
      "disabled",
      false,
    ),
  );
  expect(screen.getAllByLabelText("work")[0]).toHaveProperty("checked", true);
  expect(screen.getAllByLabelText("work")[1]).toHaveProperty("checked", false);
  fireEvent.click(screen.getAllByLabelText("work")[1]);
  expect(latest[0].profiles).toEqual(["work"]);
  expect(latest[1].profiles).toEqual(["personal", "work"]);
  expect(
    screen.getAllByRole("option", {
      name: "Notion",
    })[0],
  ).toHaveProperty("disabled", false);
  fireEvent.change(screen.getAllByLabelText("Service")[1], {
    target: { value: "agent-notion" },
  });
  expect(latest[0].profiles).toEqual(["work"]);
  expect(latest[1].tool).toBe("agent-notion");
  expect(latest[1].profiles).toEqual([]);
  expect(await screen.findByText("Uses the CLI default account")).toBeTruthy();
  expect(screen.getByText("Uses the account its CLI has")).toBeTruthy();
  expect(screen.getAllByLabelText("Name")[0]).toHaveProperty("maxLength", 80);
});
it("starts a suggestion, then sends a trimmed answer without submitting a form", async () => {
  let release: () => void = () => {};
  response = (path) =>
    path.endsWith("/interview")
      ? {
          messages: [{ role: "assistant", content: "How should I sound?" }],
          questions: [],
        }
      : { messages: [], questions: [] };
  render(
    <AssistantSetup currentName="Iris" demo={false} onApplied={vi.fn()} />,
  );
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const stub = vi.mocked(fetch);
  stub.mockImplementationOnce(async (path, options) => {
    requests.push({ path: String(path), options });
    await gate;
    return new Response(JSON.stringify(response(String(path))));
  });
  fireEvent.click(await screen.findByRole("button", { name: "Start" }));
  expect((await screen.findByRole("status")).textContent).toBe(
    "Working on a suggestion…",
  );
  release();
  const answer = await screen.findByLabelText("Your answer");
  expect(screen.getByText("How should I sound?")).toBeTruthy();
  const send = screen.getByRole("button", { name: "Send" });
  expect(send.getAttribute("type")).toBe("button");
  expect(send).toHaveProperty("disabled", true);
  fireEvent.change(answer, { target: { value: "  Calm and short.  " } });
  fireEvent.click(send);
  await waitFor(() =>
    expect(requests.filter((r) => r.path.endsWith("/interview"))).toHaveLength(
      2,
    ),
  );
  const bodies = requests
    .filter((r) => r.path.endsWith("/interview"))
    .map((r) => JSON.parse(String(r.options?.body)));
  expect(bodies).toEqual([{ message: "" }, { message: "Calm and short." }]);
  await waitFor(() => expect(answer).toHaveProperty("value", ""));
});
it("offers no suggestion in the demo", async () => {
  response = () => ({ messages: [], questions: [], recommendation });
  render(<AssistantSetup currentName="Iris" demo onApplied={vi.fn()} />);
  expect(screen.getByText("Not available in the demo.")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Start" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(
    await screen.findByRole("button", { name: "Use this" }),
  ).toHaveProperty("disabled", true);
  expect(requests.every((r) => r.path === "/api/setup")).toBe(true);
});

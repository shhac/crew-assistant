// @vitest-environment jsdom
import { useState } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { SlackBotSettings } from "./SlackBotSettings";
import { AdvancedSettings } from "./AdvancedSettings";
import { type Config, type Project } from "./api";

afterEach(cleanup);
const projects: Project[] = [
  {
    id: "backend",
    title: "Synthetic Backend",
    status: "active",
    brief: { version: 1, goal: "Develop the product", criteria: [] },
  },
  {
    id: "docs",
    title: "Synthetic Docs",
    status: "active",
    brief: { version: 1, goal: "Write documentation", criteria: [] },
    playbook: {
      template: "draft",
      medium: "documents",
      roles: [{ name: "Pia", engine: "claude", kinds: ["pm"] }],
      max_rounds: 3,
      deliver: "owner",
    },
  },
];

it("edits the Slack destination and variable names while preserving other settings", () => {
  let latest: Config;
  const initial = {
    assistant: { seat: "iris" },
    slack: { owner_user_id: "U_SYNTHETIC", other: "preserved" },
  } as Config;
  function Editor() {
    const [config, setConfig] = useState(initial);
    latest = config;
    return (
      <SlackBotSettings
        config={config}
        projects={projects}
        onChange={setConfig}
      />
    );
  }
  render(<Editor />);
  expect(screen.queryByRole("combobox")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  expect(
    screen.getByRole("button", { name: "Close" }).getAttribute("aria-expanded"),
  ).toBe("true");
  const destination = screen.getByRole<HTMLSelectElement>("combobox", {
    name: "Destination",
  });
  expect(Array.from(destination.options).map((o) => [o.value, o.text])).toEqual(
    [
      ["", "Assistant"],
      ["backend", "Synthetic Backend (Project manager)"],
      ["docs", "Synthetic Docs (Project manager)"],
    ],
  );
  fireEvent.change(destination, { target: { value: "backend" } });
  expect(screen.getByText(/Choose a project manager in/)).toBeTruthy();
  expect(
    screen
      .getByRole("link", { name: "Synthetic Backend's team" })
      .getAttribute("href"),
  ).toBe("#/projects/backend/team");
  fireEvent.change(destination, { target: { value: "docs" } });
  expect(screen.queryByText(/Choose a project manager in/)).toBeNull();
  fireEvent.change(
    screen.getByRole("textbox", { name: "Slack workspace ID" }),
    { target: { value: "T_SYNTHETIC" } },
  );
  fireEvent.change(
    screen.getByRole("textbox", { name: "Bot token variable" }),
    { target: { value: "CUSTOM_BOT_TOKEN" } },
  );
  fireEvent.change(
    screen.getByRole("textbox", { name: "App token variable" }),
    { target: { value: "CUSTOM_APP_TOKEN" } },
  );
  expect(latest!).toEqual({
    ...initial,
    slack: {
      owner_user_id: "U_SYNTHETIC",
      other: "preserved",
      workspace_id: "T_SYNTHETIC",
      project_id: "docs",
      bot_token_env: "CUSTOM_BOT_TOKEN",
      app_token_env: "CUSTOM_APP_TOKEN",
    },
  });
  expect(
    screen.getAllByText("The environment variable's name, never the token."),
  ).toHaveLength(2);
  expect(screen.getAllByRole("textbox")).toHaveLength(4);
  expect(
    screen.getByRole<HTMLInputElement>("textbox", {
      name: "Bot token variable",
    }).pattern,
  ).toBe("[A-Za-z_][A-Za-z0-9_]*");
  expect(
    screen.getByText(/Restart crew-assistant after changing this connection/),
  ).toBeTruthy();
  fireEvent.change(destination, { target: { value: "" } });
  expect((latest!.slack as Record<string, unknown>).project_id).toBe("");
});

it("shows restart status and preserves an unavailable saved destination", () => {
  render(
    <SlackBotSettings
      config={{ slack: { project_id: "missing" } } as Config}
      projects={projects}
      integration={{
        id: "slack",
        name: "Slack bot messaging",
        status: "restart_required",
        detail: "Restart to apply the saved connection.",
      }}
      onChange={() => {}}
    />,
  );
  expect(screen.getByText("Restart required")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  expect(
    screen.getByRole<HTMLSelectElement>("combobox", { name: "Destination" })
      .value,
  ).toBe("missing");
});

it("keeps Slack messaging out of Advanced and leaves direct Linear settings", () => {
  render(<AdvancedSettings config={{} as Config} onChange={() => {}} />);
  expect(screen.queryByText(/Slack bot/)).toBeNull();
  expect(
    screen.queryByRole("textbox", { name: "Bot token variable" }),
  ).toBeNull();
  expect(
    screen.getByRole("textbox", { name: "API key variable" }),
  ).toBeTruthy();
});

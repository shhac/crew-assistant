// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { FileSystemPicker } from "./FileSystemPicker";
import { ConfigTab } from "./ProjectConfig";
import { DirectoryList } from "./ProjectFolders";
import { NewProject } from "./ProjectForms";
import type { FileSystemPage, Project } from "./api";
let calls: URL[];
let writes: { path: string; body: unknown }[];
let respond: (url: URL) => FileSystemPage | { error: string };
let respondWrite: (path: string) => { status: number; body: unknown };
const entry = (
  path: string,
  kind: "directory" | "file" = "directory",
  selectable = true,
) => ({ path, name: path.split("/").pop()!, kind, selectable });
const listing = (
  path: string,
  entries: ReturnType<typeof entry>[] = [],
  next_cursor: string | null = null,
): FileSystemPage => ({
  path,
  parent: path === "/" ? null : "/",
  entries,
  next_cursor,
});
beforeEach(() => {
  calls = [];
  writes = [];
  respondWrite = (path) =>
    path === "/api/projects"
      ? { status: 200, body: { id: "project-new", title: "created" } }
      : { status: 200, body: {} };
  respond = () =>
    listing("/home", [entry("/home/work"), entry("/home/personal")]);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, options?: RequestInit) => {
      const url = new URL(input, "http://localhost");
      if (options?.method && options.method !== "GET") {
        writes.push({
          path: url.pathname,
          body: JSON.parse(String(options.body)),
        });
        const reply = respondWrite(url.pathname);
        return {
          ok: reply.status < 400,
          status: reply.status,
          json: async () => reply.body,
        };
      }
      calls.push(url);
      const body = respond(url);
      return {
        ok: !("error" in body),
        status: "error" in body ? 403 : 200,
        json: async () => body,
      };
    }),
  );
  HTMLDialogElement.prototype.showModal = function () {
    this.setAttribute("open", "");
  };
  HTMLDialogElement.prototype.close = function () {
    this.removeAttribute("open");
  };
  HTMLElement.prototype.scrollIntoView = vi.fn();
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
    width: 600,
    height: 342,
    top: 0,
    left: 0,
    right: 600,
    bottom: 342,
    x: 0,
    y: 0,
    toJSON: () => ({}),
  });
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(342);
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(600);
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
it("lazily expands directories and keeps multiple selections", async () => {
  respond = (url) =>
    url.searchParams.get("path") === "/home/work"
      ? listing("/home/work", [entry("/home/work/repo")])
      : listing("/home", [entry("/home/work"), entry("/home/personal")]);
  const select = vi.fn();
  render(
    <FileSystemPicker
      kind="directory"
      multiple
      onSelect={select}
      onCancel={vi.fn()}
    />,
  );
  const work = await screen.findByRole("treeitem", { name: "work" });
  expect(calls).toHaveLength(1);
  fireEvent.click(work);
  await screen.findByRole("treeitem", { name: "repo" });
  expect(
    calls.some((url) => url.searchParams.get("path") === "/home/work"),
  ).toBe(true);
  fireEvent.click(screen.getByRole("treeitem", { name: "personal" }), {
    ctrlKey: true,
  });
  expect(screen.getByText("2 selected")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Use these" }));
  expect(select).toHaveBeenCalledWith(["/home/work", "/home/personal"]);
});
it("uses canonical paths and cursors for paginated alias folders", async () => {
  respond = (url) => {
    const path = url.searchParams.get("path");
    if (!path) return listing("/home", [entry("/home/alias")]);
    if (path === "/home/alias")
      return listing(
        "/real/project",
        [entry("/real/project/first", "file")],
        "next-page",
      );
    if (
      path === "/real/project" &&
      url.searchParams.get("cursor") === "next-page"
    )
      return listing(path, [entry("/real/project/second", "file")]);
    throw new Error(`Unexpected filesystem query ${url}`);
  };
  render(<FileSystemPicker kind="any" onSelect={vi.fn()} onCancel={vi.fn()} />);
  fireEvent.click(await screen.findByRole("treeitem", { name: "alias" }));
  fireEvent.click(await screen.findByRole("treeitem", { name: "Show more" }));
  expect(await screen.findByRole("treeitem", { name: "second" })).toBeTruthy();
  expect(screen.getByRole("treeitem", { name: "first" })).toBeTruthy();
  expect(calls.at(-1)?.searchParams.get("cursor")).toBe("next-page");
  expect(calls.at(-1)?.searchParams.get("path")).toBe("/real/project");
});
it("supports current empty folders, path jumps, hidden entries, and error retry", async () => {
  let denied = true;
  respond = (url) =>
    url.searchParams.get("path") === "/private" && denied
      ? { error: "Folder is unavailable" }
      : listing(url.searchParams.get("path") || "/home");
  const select = vi.fn();
  render(
    <FileSystemPicker kind="directory" onSelect={select} onCancel={vi.fn()} />,
  );
  await screen.findByRole("button", { name: "Choose this folder: /home" });
  fireEvent.change(screen.getByLabelText("Path"), {
    target: { value: "/private" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Go" }));
  await screen.findByRole("alert");
  denied = false;
  fireEvent.click(screen.getByRole("button", { name: "Try again" }));
  await screen.findByRole("button", { name: "Choose this folder: /private" });
  fireEvent.click(screen.getByLabelText("Show hidden"));
  fireEvent.click(
    await screen.findByRole("button", { name: "Choose this folder: /private" }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Use this" }));
  expect(select).toHaveBeenCalledWith(["/private"]);
  expect(calls.at(-1)?.searchParams.get("hidden")).toBe("true");
});
it("file mode traverses folders without selecting them and enforces one selection", async () => {
  respond = (url) =>
    url.searchParams.get("path") === "/home/folder"
      ? listing("/home/folder", [
          entry("/home/folder/a.txt", "file"),
          entry("/home/folder/b.txt", "file"),
        ])
      : listing("/home", [entry("/home/folder", "directory", false)]);
  const select = vi.fn();
  render(<FileSystemPicker kind="file" onSelect={select} onCancel={vi.fn()} />);
  fireEvent.click(await screen.findByRole("treeitem", { name: "folder" }));
  expect(screen.getByRole("button", { name: "Use this" })).toHaveProperty(
    "disabled",
    true,
  );
  fireEvent.click(await screen.findByRole("treeitem", { name: "a.txt" }));
  fireEvent.click(screen.getByRole("treeitem", { name: "b.txt" }), {
    ctrlKey: true,
  });
  fireEvent.click(screen.getByRole("button", { name: "Use this" }));
  expect(select).toHaveBeenCalledWith(["/home/folder/b.txt"]);
});
it("virtualizes large folders rather than mounting every entry", async () => {
  respond = () =>
    listing(
      "/home",
      Array.from({ length: 250 }, (_, i) => entry(`/home/folder-${i}`)),
    );
  render(
    <FileSystemPicker kind="directory" onSelect={vi.fn()} onCancel={vi.fn()} />,
  );
  await screen.findByRole("treeitem", { name: "folder-0" });
  expect(screen.getAllByRole("treeitem").length).toBeLessThan(40);
});
const trackedProject = (directories: string[]): Project => ({
  id: "project-1",
  title: "Existing work",
  status: "tracking",
  brief: { version: 0, goal: "", criteria: [] },
  directories,
  scratch_directory: "/state/scratch/project-1",
});
async function pickCurrentFolder(path: string, confirm = "Use this") {
  fireEvent.click(
    await screen.findByRole("button", { name: `Choose this folder: ${path}` }),
  );
  fireEvent.click(screen.getByRole("button", { name: confirm }));
}
it("adds an existing folder without requiring an outcome or starting coordination", async () => {
  respond = () => listing("/home/existing-repo");
  const created = vi.fn(async () => {});
  render(<NewProject onClose={vi.fn()} onCreated={created} />);
  fireEvent.click(screen.getByRole("button", { name: "Tracking only" }));
  fireEvent.click(screen.getByRole("button", { name: "Add folders" }));
  await pickCurrentFolder("/home/existing-repo");
  expect(screen.getByLabelText("Name")).toHaveProperty(
    "value",
    "existing-repo",
  );
  fireEvent.click(screen.getByRole("button", { name: "Create project" }));
  await waitFor(() => expect(created).toHaveBeenCalledWith("project-new"));
  expect(writes).toEqual([
    {
      path: "/api/projects",
      body: {
        title: "existing-repo",
        directories: ["/home/existing-repo"],
        brief: { goal: "", audience: "", constraints: "", criteria: [] },
        template: "",
      },
    },
  ]);
});
it("keeps attached-directory edits until explicit save", async () => {
  const refresh = vi.fn(async () => {});
  render(
    <ConfigTab
      project={trackedProject(["/home/work"])}
      members={[]}
      refresh={refresh}
    />,
  );
  const folders = screen.getByRole("region", { name: "Folders" });
  expect(folders.textContent).toContain("/home/work");
  expect(
    screen.queryByRole("button", { name: "Remove folder /home/work" }),
  ).toBeNull();
  fireEvent.click(within(folders).getByRole("button", { name: "Edit" }));
  fireEvent.click(
    screen.getByRole("button", { name: "Remove folder /home/work" }),
  );
  expect(within(folders).getByText("No folders.")).toBeTruthy();
  expect(writes).toHaveLength(0);
  fireEvent.click(screen.getByRole("button", { name: "Save folders" }));
  await waitFor(() => expect(refresh).toHaveBeenCalled());
  expect(writes).toEqual([
    { path: "/api/projects/project-1/directories", body: { directories: [] } },
  ]);
});
it("adds picked folders to the project only when saved, and cancel discards edits", async () => {
  respond = (url) => listing(url.searchParams.get("path") || "/home/work");
  const refresh = vi.fn(async () => {});
  render(
    <ConfigTab
      project={trackedProject(["/home/work"])}
      members={[]}
      refresh={refresh}
    />,
  );
  const folders = screen.getByRole("region", { name: "Folders" });
  fireEvent.click(within(folders).getByRole("button", { name: "Edit" }));
  fireEvent.click(
    screen.getByRole("button", { name: "Remove folder /home/work" }),
  );
  fireEvent.click(within(folders).getByRole("button", { name: "Cancel" }));
  expect(folders.textContent).toContain("/home/work");
  expect(writes).toHaveLength(0);

  fireEvent.click(within(folders).getByRole("button", { name: "Edit" }));
  fireEvent.click(within(folders).getByRole("button", { name: "Add folders" }));
  await screen.findByRole("button", { name: "Choose this folder: /home/work" });
  expect(calls.at(-1)?.searchParams.get("path")).toBe("/home/work");
  fireEvent.change(screen.getByLabelText("Path"), {
    target: { value: "/home/notes" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Go" }));
  await pickCurrentFolder("/home/notes");
  expect(
    screen.getByRole("button", { name: "Remove folder /home/notes" }),
  ).toBeTruthy();
  expect(writes).toHaveLength(0);
  fireEvent.click(screen.getByRole("button", { name: "Save folders" }));
  await waitFor(() => expect(refresh).toHaveBeenCalled());
  expect(writes).toEqual([
    {
      path: "/api/projects/project-1/directories",
      body: { directories: ["/home/work", "/home/notes"] },
    },
  ]);
});
it("offers to add folders to a project that has none", () => {
  render(
    <ConfigTab project={trackedProject([])} members={[]} refresh={vi.fn()} />,
  );
  const folders = screen.getByRole("region", { name: "Folders" });
  expect(within(folders).getByText("No folders.")).toBeTruthy();
  fireEvent.click(within(folders).getByRole("button", { name: "Add folders" }));
  expect(
    within(folders).getByRole("button", { name: "Save folders" }),
  ).toBeTruthy();
  expect(writes).toHaveLength(0);
});
it("lists folders read-only unless removal is offered", () => {
  const removed = vi.fn();
  const view = render(<DirectoryList paths={["/a", "/b"]} />);
  expect(screen.getByText("/a")).toBeTruthy();
  expect(screen.queryAllByRole("button")).toHaveLength(0);
  view.rerender(<DirectoryList paths={["/a", "/b"]} onRemove={removed} />);
  fireEvent.click(screen.getByRole("button", { name: "Remove folder /b" }));
  expect(removed).toHaveBeenCalledWith("/b");
});

it("keeps repeated aliases and self-links as separate lazy tree occurrences", async () => {
  respond = (url) =>
    !url.searchParams.get("path")
      ? listing("/home", [entry("/home/alias-a"), entry("/home/alias-b")])
      : listing("/real", [entry("/real/self")]);
  render(
    <FileSystemPicker
      kind="directory"
      multiple
      onSelect={vi.fn()}
      onCancel={vi.fn()}
    />,
  );
  fireEvent.click(await screen.findByRole("treeitem", { name: "alias-a" }));
  await screen.findByRole("treeitem", { name: "self" });
  fireEvent.click(screen.getByRole("treeitem", { name: "alias-b" }));
  await waitFor(() =>
    expect(screen.getAllByRole("treeitem", { name: "self" })).toHaveLength(2),
  );
  fireEvent.click(screen.getAllByRole("treeitem", { name: "self" })[0]);
  await waitFor(() =>
    expect(screen.getAllByRole("treeitem", { name: "self" })).toHaveLength(3),
  );
  expect(calls).toHaveLength(4);
  expect(
    screen
      .getAllByRole("treeitem")
      .map((item) => item.getAttribute("aria-level")),
  ).toEqual(["1", "2", "3", "1", "2"]);
});
it("supports arrow-key folder expansion without selecting an entry", async () => {
  respond = (url) =>
    url.searchParams.get("path") === "/home/work"
      ? listing("/home/work", [entry("/home/work/repo")])
      : listing("/home", [entry("/home/work")]);
  render(
    <FileSystemPicker kind="directory" onSelect={vi.fn()} onCancel={vi.fn()} />,
  );
  const work = await screen.findByRole("treeitem", { name: "work" });
  work.focus();
  fireEvent.keyDown(work, { key: "ArrowRight", code: "ArrowRight" });
  fireEvent.keyUp(work, { key: "ArrowRight", code: "ArrowRight" });
  await screen.findByRole("treeitem", { name: "repo" });
  expect(screen.getByRole("button", { name: "Use this" })).toHaveProperty(
    "disabled",
    true,
  );
});
it("needs only a name to track a project, and a goal for written work", async () => {
  respond = () => listing("/home/existing-repo");
  render(<NewProject onClose={vi.fn()} onCreated={vi.fn()} />);
  const kinds = screen.getByRole("group", { name: "Kind of project" });
  expect(
    within(kinds)
      .getAllByRole("button")
      .map((button) => button.textContent),
  ).toEqual(["Writing", "Code", "Tracking only"]);
  expect(
    within(kinds)
      .getByRole("button", { name: "Writing" })
      .getAttribute("aria-pressed"),
  ).toBe("true");
  fireEvent.click(screen.getByRole("button", { name: "Add folders" }));
  await pickCurrentFolder("/home/existing-repo");
  const create = screen.getByRole("button", { name: "Create project" });
  expect(create).toHaveProperty("disabled", true);
  expect(screen.getByLabelText("Goal")).toHaveProperty("required", true);
  fireEvent.click(screen.getByRole("button", { name: "Tracking only" }));
  expect(create).toHaveProperty("disabled", false);
  expect(screen.getByLabelText("Goal (optional)")).toHaveProperty(
    "required",
    false,
  );
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: " " } });
  expect(create).toHaveProperty("disabled", true);
  // The rest of the brief stays out of the way until the owner asks for it.
  const more = screen.getByText("More about the brief");
  expect(more.closest("details")?.open).toBe(false);
  expect(writes).toHaveLength(0);
});
it("creates written work with the draft team and its brief", async () => {
  const created = vi.fn(async () => {});
  render(<NewProject onClose={vi.fn()} onCreated={created} />);
  fireEvent.change(screen.getByLabelText("Name"), {
    target: { value: " Launch note " },
  });
  fireEvent.change(screen.getByLabelText("Goal"), {
    target: { value: " Announce the launch " },
  });
  fireEvent.change(screen.getByLabelText("Done when"), {
    target: { value: "Short\n\n Friendly " },
  });
  fireEvent.click(screen.getByRole("button", { name: "Create project" }));
  await waitFor(() => expect(created).toHaveBeenCalledWith("project-new"));
  expect(writes).toEqual([
    {
      path: "/api/projects",
      body: {
        title: "Launch note",
        directories: [],
        brief: {
          goal: "Announce the launch",
          audience: "",
          constraints: "",
          criteria: ["Short", "Friendly"],
        },
        template: "draft",
      },
    },
  ]);
});
it("creates a code project, then sets its code team on the chosen repository", async () => {
  respond = (url) => listing(url.searchParams.get("path") || "/home/repo");
  const created = vi.fn(async () => {});
  render(<NewProject onClose={vi.fn()} onCreated={created} />);
  fireEvent.click(screen.getByRole("button", { name: "Code" }));
  expect(screen.queryByRole("button", { name: "Add folders" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Choose repository" }));
  await pickCurrentFolder("/home/repo");
  expect(screen.getByLabelText("Name")).toHaveProperty("value", "repo");
  expect(
    screen.getByRole("button", { name: "Remove folder /home/repo" }),
  ).toBeTruthy();
  const create = screen.getByRole("button", { name: "Create project" });
  fireEvent.change(screen.getByLabelText("Goal"), {
    target: { value: "Fix the flaky login test" },
  });
  expect(create).toHaveProperty("disabled", true);
  expect(screen.getByLabelText("QA runs")).toHaveProperty("required", true);
  fireEvent.change(screen.getByLabelText("QA runs"), {
    target: { value: " make check " },
  });
  expect(create).toHaveProperty("disabled", false);

  fireEvent.click(screen.getByRole("button", { name: "Change repository" }));
  await screen.findByRole("button", { name: "Choose this folder: /home/repo" });
  expect(calls.at(-1)?.searchParams.get("path")).toBe("/home/repo");
  fireEvent.change(screen.getByLabelText("Path"), {
    target: { value: "/home/other" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Go" }));
  await pickCurrentFolder("/home/other");
  expect(
    screen.queryByRole("button", { name: "Remove folder /home/repo" }),
  ).toBeNull();
  expect(screen.getByLabelText("Name")).toHaveProperty("value", "repo");

  fireEvent.click(create);
  await waitFor(() => expect(created).toHaveBeenCalledWith("project-new"));
  expect(writes).toEqual([
    {
      path: "/api/projects",
      body: {
        title: "repo",
        directories: ["/home/other"],
        brief: {
          goal: "Fix the flaky login test",
          audience: "",
          constraints: "",
          criteria: [],
        },
        template: "",
      },
    },
    {
      path: "/api/projects/project-new/team",
      body: {
        template: "code",
        writer_engine: "",
        reviewer_engine: "",
        max_rounds: "",
        deliver_to: "",
        repo: "/home/other",
        branch_prefix: "",
        check: "make check",
        prepare: [],
        sign: "",
      },
    },
  ]);
});
it("keeps a created code project reachable when its team cannot be set", async () => {
  respond = () => listing("/home/repo");
  respondWrite = (path) =>
    path === "/api/projects"
      ? { status: 200, body: { id: "project-new" } }
      : { status: 400, body: { error: "The repository has no commits" } };
  const created = vi.fn(async () => {});
  render(<NewProject onClose={vi.fn()} onCreated={created} />);
  fireEvent.click(screen.getByRole("button", { name: "Code" }));
  fireEvent.click(screen.getByRole("button", { name: "Choose repository" }));
  await pickCurrentFolder("/home/repo");
  fireEvent.change(screen.getByLabelText("Goal"), {
    target: { value: "Ship it" },
  });
  fireEvent.change(screen.getByLabelText("QA runs"), {
    target: { value: "make check" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Create project" }));
  const heading = await screen.findByRole("heading", {
    name: "The project was made without its team",
  });
  expect(heading.closest("dialog")?.hasAttribute("open")).toBe(true);
  expect(screen.getByText("The repository has no commits")).toBeTruthy();
  expect(created).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Open the project" }));
  expect(created).toHaveBeenCalledWith("project-new");
  expect(writes.map((write) => write.path)).toEqual([
    "/api/projects",
    "/api/projects/project-new/team",
  ]);
});
it("names the computer being browsed and the button that chooses the open folder", async () => {
  respond = () => listing("/home", [entry("/home/work")]);
  const cancel = vi.fn();
  render(
    <FileSystemPicker
      kind="directory"
      multiple
      onCancel={cancel}
      onSelect={vi.fn()}
    />,
  );
  await screen.findByRole("treeitem", { name: "work" });
  expect(screen.getByRole("heading", { name: "Choose folders" })).toBeTruthy();
  expect(
    screen.getByText("On the computer running crew-assistant."),
  ).toBeTruthy();
  expect(screen.getByRole("tree", { name: "Folders and files" })).toBeTruthy();
  expect(screen.getByLabelText("Path")).toHaveProperty(
    "placeholder",
    "Home folder",
  );
  expect(screen.queryByText(/Clicking a folder/)).toBeNull();
  const select = screen.getByRole("button", {
    name: "Choose this folder: /home",
  });
  expect(select.querySelector("code")?.textContent).toBe("/home");
  expect(select.className).toContain("btn");
  expect(select.className).not.toContain("link-button");
  expect(screen.getByText("Nothing selected")).toBeTruthy();
  fireEvent.click(select);
  expect(screen.getByText("1 selected")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Use this" })).toHaveProperty(
    "disabled",
    false,
  );
  fireEvent.click(screen.getByRole("button", { name: "Close" }));
  expect(cancel).toHaveBeenCalledTimes(1);
});
it("goes up a folder and says so when a folder is empty", async () => {
  respond = (url) => listing(url.searchParams.get("path") || "/home/empty");
  render(
    <FileSystemPicker kind="directory" onSelect={vi.fn()} onCancel={vi.fn()} />,
  );
  expect(await screen.findByText("Nothing here.")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Up a folder" }));
  await screen.findByRole("button", { name: "Choose this folder: /" });
  expect(calls.at(-1)?.searchParams.get("path")).toBe("/");
  expect(screen.getByRole("button", { name: "Up a folder" })).toHaveProperty(
    "disabled",
    true,
  );
});
it("offers to try a folder again when its entries fail to load", async () => {
  let failing = true;
  respond = (url) => {
    const path = url.searchParams.get("path");
    if (path === "/home/locked" && failing)
      return { error: "Permission denied" };
    if (path === "/home/locked")
      return listing("/home/locked", [entry("/home/locked/inside")]);
    return listing("/home", [entry("/home/locked")]);
  };
  render(
    <FileSystemPicker kind="directory" onSelect={vi.fn()} onCancel={vi.fn()} />,
  );
  fireEvent.click(await screen.findByRole("treeitem", { name: "locked" }));
  const retry = await screen.findByRole("treeitem", { name: "Try again" });
  expect(await screen.findByText("Permission denied")).toBeTruthy();
  failing = false;
  fireEvent.click(retry);
  expect(await screen.findByRole("treeitem", { name: "inside" })).toBeTruthy();
  expect(screen.queryByRole("treeitem", { name: "Try again" })).toBeNull();
  await screen.findByText("Loaded");
});
it("says a paginated list is out of date when its cursor has expired", async () => {
  respond = () => listing("/home", [entry("/home/a", "file")], "page-2");
  const fetch = vi.mocked(globalThis.fetch);
  const original = fetch.getMockImplementation()!;
  fetch.mockImplementation(async (input, options) => {
    const url = new URL(String(input), "http://localhost");
    if (url.searchParams.get("cursor")) {
      calls.push(url);
      return new Response(JSON.stringify({ error: "stale" }), { status: 409 });
    }
    return original(input, options);
  });
  render(<FileSystemPicker kind="any" onSelect={vi.fn()} onCancel={vi.fn()} />);
  fireEvent.click(await screen.findByRole("treeitem", { name: "Show more" }));
  expect(
    await screen.findByText(
      "This list is out of date. Open the folder again to reload it.",
    ),
  ).toBeTruthy();
  expect(calls.at(-1)?.searchParams.get("cursor")).toBe("page-2");
});

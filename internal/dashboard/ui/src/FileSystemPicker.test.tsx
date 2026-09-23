// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { FileSystemPicker } from "./FileSystemPicker";
import { NewProject, ProjectDirectories } from "./ProjectForms";
import type { FileSystemPage } from "./api";
let calls: URL[];
let writes: { path: string; body: unknown }[];
let respond: (url: URL) => FileSystemPage | { error: string };
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
  respond = () =>
    listing("/home", [entry("/home/work"), entry("/home/personal")]);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, options?: RequestInit) => {
      const url = new URL(input, "http://localhost");
      if (options?.method && options.method !== "GET") {
        writes.push({
          path: url.pathname,
          body: JSON.parse(options.body as string),
        });
        return { ok: true, status: 200, json: async () => ({}) };
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
  fireEvent.click(screen.getByRole("button", { name: "Use selection" }));
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
  fireEvent.click(
    await screen.findByRole("treeitem", { name: "Load more entries" }),
  );
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
  await screen.findByRole("button", { name: "Select this folder: /home" });
  fireEvent.change(screen.getByLabelText("Folder path"), {
    target: { value: "/private" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Go" }));
  await screen.findByRole("alert");
  denied = false;
  fireEvent.click(screen.getByRole("button", { name: "Retry folder" }));
  await screen.findByRole("button", { name: "Select this folder: /private" });
  fireEvent.click(screen.getByLabelText("Show hidden entries"));
  fireEvent.click(
    await screen.findByRole("button", { name: "Select this folder: /private" }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Use selection" }));
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
  expect(screen.getByRole("button", { name: "Use selection" })).toHaveProperty(
    "disabled",
    true,
  );
  fireEvent.click(await screen.findByRole("treeitem", { name: "a.txt" }));
  fireEvent.click(screen.getByRole("treeitem", { name: "b.txt" }), {
    ctrlKey: true,
  });
  fireEvent.click(screen.getByRole("button", { name: "Use selection" }));
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
it("adds an existing folder without requiring an outcome or starting coordination", async () => {
  respond = () => listing("/home/existing-repo");
  const created = vi.fn(async () => {});
  render(<NewProject onClose={vi.fn()} onCreated={created} />);
  fireEvent.click(screen.getByRole("button", { name: "Just track it" }));
  fireEvent.click(screen.getByRole("button", { name: "Choose folders" }));
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Select this folder: /home/existing-repo",
    }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Use selection" }));
  expect(screen.getByLabelText("Project name")).toHaveProperty(
    "value",
    "existing-repo",
  );
  fireEvent.click(screen.getByRole("button", { name: "Create project" }));
  await waitFor(() => expect(created).toHaveBeenCalled());
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
    <ProjectDirectories
      project={{
        id: "project-1",
        title: "Existing work",
        status: "tracking",
        brief: { version: 0, goal: "", criteria: [] },
        directories: ["/home/work"],
        scratch_directory: "/state/scratch/project-1",
      }}
      refresh={refresh}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Edit folders" }));
  fireEvent.click(
    screen.getByRole("button", { name: "Remove folder /home/work" }),
  );
  expect(writes).toHaveLength(0);
  fireEvent.click(screen.getByRole("button", { name: "Save folders" }));
  await waitFor(() => expect(refresh).toHaveBeenCalled());
  expect(writes).toEqual([
    { path: "/api/projects/project-1/directories", body: { directories: [] } },
  ]);
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
  expect(screen.getByRole("button", { name: "Use selection" })).toHaveProperty(
    "disabled",
    true,
  );
});
it("needs only a name to track a project, and a goal for written work", async () => {
  respond = () => listing("/home/existing-repo");
  render(<NewProject onClose={vi.fn()} onCreated={vi.fn()} />);
  fireEvent.click(screen.getByRole("button", { name: "Choose folders" }));
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Select this folder: /home/existing-repo",
    }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Use selection" }));
  const create = screen.getByRole("button", { name: "Create project" });
  expect(create).toHaveProperty("disabled", true);
  expect(screen.getByLabelText("Goal")).toHaveProperty("required", true);
  fireEvent.click(screen.getByRole("button", { name: "Just track it" }));
  expect(create).toHaveProperty("disabled", false);
  expect(screen.getByLabelText("Goal (optional)")).toHaveProperty(
    "required",
    false,
  );
  // The rest of the brief stays out of the way until the owner asks for it.
  const more = screen.getByText("More about the brief (optional)");
  expect((more.parentElement as HTMLDetailsElement).open).toBe(false);
});
it("says plainly that clicking opens a folder and the button selects it", async () => {
  respond = () => listing("/home", [entry("/home/work")]);
  render(
    <FileSystemPicker
      kind="directory"
      multiple
      onCancel={vi.fn()}
      onSelect={vi.fn()}
    />,
  );
  await screen.findByRole("treeitem", { name: "work" });
  expect(
    screen.getByText(/Clicking a folder in the list/).textContent,
  ).toContain("opens");
  const select = screen.getByRole("button", {
    name: "Select this folder: /home",
  });
  expect(select.className).toContain("button");
  expect(select.className).not.toContain("text-button");
});

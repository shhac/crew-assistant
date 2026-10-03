// @vitest-environment node
import { afterEach, beforeEach, expect, test } from "vitest";
import { mkdir, mkdtemp, readdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import {
  checkBundle,
  compareBundles,
  formatDifferences,
} from "./bundle-fresh.mjs";

let root, built, committed;
async function put(dir, name, content) {
  await mkdir(path.dirname(path.join(dir, name)), { recursive: true });
  await writeFile(path.join(dir, name), content);
}
async function fixture(dir) {
  await put(
    dir,
    "index.html",
    '<script src="/generated/index-abc.js"></script>',
  );
  await put(dir, "generated/index-abc.js", "hello");
}
beforeEach(async () => {
  root = await mkdtemp(path.join(os.tmpdir(), "bundle-test-"));
  built = path.join(root, "built");
  committed = path.join(root, "repo/internal/dashboard/assets");
  await fixture(built);
  await fixture(committed);
});
afterEach(async () => {
  await rm(root, { recursive: true, force: true });
});

test("fresh bundles pass comparison and the CLI result, cleaning temporary output", async () => {
  expect(await compareBundles(built, committed)).toEqual({
    missing: [],
    extra: [],
    differing: [],
  });
  const result = await checkBundle({
    committedDir: committed,
    tempRoot: root,
    build: async (options) => {
      expect(options.configLoader).toBe("runner");
      expect(options.cacheDir.startsWith(root)).toBe(true);
      await fixture(options.build.outDir);
    },
  });
  expect(result).toEqual({ code: 0, message: "" });
  expect(
    (await readdir(root)).filter((name) => name.startsWith("crew-dashboard-")),
  ).toEqual([]);
});

test("changed bytes fail with the affected path and rebuild instruction", async () => {
  await put(committed, "generated/index-abc.js", "hellO");
  const differences = await compareBundles(built, committed);
  expect(differences.differing).toEqual(["generated/index-abc.js"]);
  expect(formatDifferences(differences)).toContain("make dashboard");
  const result = await checkBundle({
    committedDir: committed,
    tempRoot: root,
    build: async (options) => fixture(options.build.outDir),
  });
  expect(result.code).toBe(1);
  expect(result.message).toContain("differing: generated/index-abc.js");
});

test("renamed content hashes report missing and extra files", async () => {
  await rm(path.join(built, "generated/index-abc.js"));
  await put(built, "generated/index-new.js", "hello");
  expect(await compareBundles(built, committed)).toEqual({
    missing: ["generated/index-new.js"],
    extra: ["generated/index-abc.js"],
    differing: [],
  });
});

test("an index.html-only change is caught", async () => {
  await put(
    built,
    "index.html",
    '<script src="/generated/index-new.js"></script>',
  );
  expect((await compareBundles(built, committed)).differing).toEqual([
    "index.html",
  ]);
});

test("changes outside assets have no effect", async () => {
  await put(path.join(root, "repo"), "cmd/main.go", "unrelated change");
  await put(
    path.join(root, "repo"),
    "internal/dashboard/ui/src/main.ts",
    "source outside comparison",
  );
  expect(formatDifferences(await compareBundles(built, committed))).toBe("");
});

test("stray files are reported, including hidden files", async () => {
  await put(committed, ".DS_Store", "stray");
  expect((await compareBundles(built, committed)).extra).toEqual([".DS_Store"]);
});

test("build errors are distinct from staleness and clean up partial output", async () => {
  const result = await checkBundle({
    committedDir: committed,
    tempRoot: root,
    build: async (options) => {
      await fixture(options.build.outDir);
      throw new Error("native binary missing");
    },
  });
  expect(result).toEqual({
    code: 1,
    message: "Dashboard build failed: native binary missing",
  });
  expect(result.message).not.toContain("make dashboard");
  expect(
    (await readdir(root)).filter((name) => name.startsWith("crew-dashboard-")),
  ).toEqual([]);
});

test("concurrent checks use independent output directories", async () => {
  const outputs = new Set();
  const build = async (options) => {
    outputs.add(options.build.outDir);
    await fixture(options.build.outDir);
  };
  expect(
    await Promise.all([
      checkBundle({ committedDir: committed, tempRoot: root, build }),
      checkBundle({ committedDir: committed, tempRoot: root, build }),
    ]),
  ).toEqual([
    { code: 0, message: "" },
    { code: 0, message: "" },
  ]);
  expect(outputs.size).toBe(2);
});

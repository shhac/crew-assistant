import { access, mkdtemp, readdir, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const uiRoot = fileURLToPath(new URL("../", import.meta.url));

async function files(dir, prefix = "") {
  const result = new Map();
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const relative = path.posix.join(prefix, entry.name);
    if (entry.isDirectory()) {
      for (const [name, bytes] of await files(
        path.join(dir, entry.name),
        relative,
      ))
        result.set(name, bytes);
    } else {
      result.set(relative, await readFile(path.join(dir, entry.name)));
    }
  }
  return result;
}

export async function compareBundles(builtDir, committedDir) {
  const [built, committed] = await Promise.all([
    files(builtDir),
    files(committedDir),
  ]);
  return {
    missing: [...built.keys()].filter((name) => !committed.has(name)).sort(),
    extra: [...committed.keys()].filter((name) => !built.has(name)).sort(),
    differing: [...built.keys()]
      .filter(
        (name) =>
          committed.has(name) && !built.get(name).equals(committed.get(name)),
      )
      .sort(),
  };
}

export function formatDifferences(result) {
  const entries = Object.entries(result).flatMap(([kind, names]) =>
    names.map((name) => `${kind}: ${name}`),
  );
  if (!entries.length) return "";
  return [
    "internal/dashboard/assets does not match a fresh build of internal/dashboard/ui: run `make dashboard` and commit internal/dashboard/assets",
    ...entries.slice(0, 20),
    ...(entries.length > 20
      ? [`... and ${entries.length - 20} more files`]
      : []),
  ].join("\n");
}

async function preparedBuild(options) {
  try {
    await access(path.join(uiRoot, "node_modules/vite/package.json"));
  } catch {
    throw new Error(
      "Missing node_modules: install the prepared dashboard dependencies before checking the bundle.",
    );
  }
  const { build } = await import("vite");
  return build(options);
}

// Injection exercises the CLI result without requiring a real Vite build.
export async function checkBundle({
  build = preparedBuild,
  committedDir = path.resolve(uiRoot, "../assets"),
  tempRoot = os.tmpdir(),
} = {}) {
  const scratch = await mkdtemp(path.join(tempRoot, "crew-dashboard-"));
  try {
    const outDir = path.join(scratch, "assets");
    try {
      await build({
        root: uiRoot,
        configFile: path.join(uiRoot, "vite.config.ts"),
        configLoader: "runner",
        cacheDir: path.join(scratch, "cache"),
        logLevel: "warn",
        build: { outDir, emptyOutDir: true },
      });
    } catch (error) {
      return {
        code: 1,
        message: `Dashboard build failed: ${error.message ?? error}`,
      };
    }
    const message = formatDifferences(
      await compareBundles(outDir, committedDir),
    );
    return { code: message ? 1 : 0, message };
  } finally {
    await rm(scratch, { recursive: true, force: true });
  }
}

if (
  process.argv[1] &&
  path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  try {
    const result = await checkBundle();
    if (result.message) console.error(result.message);
    process.exitCode = result.code;
  } catch (error) {
    console.error(`Dashboard bundle check failed: ${error.message ?? error}`);
    process.exitCode = 1;
  }
}

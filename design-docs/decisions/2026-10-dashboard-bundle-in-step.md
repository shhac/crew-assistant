# Dashboard bundle in step

Date: 2026-10-03. Base: b1525f1 (the task's supplied base).

As of this decision, four of the last forty main commits had been manual
dashboard rebuilds for release. Release checks and CI caught stale assets, but
the team's `make check` did not. A dashboard change could therefore land without
the bundle that Go embedded.

We chose implementer rebuilds, guided by a failing check, rather than rebuilds
at landing. QA ran `make check` on every draft; enforcing freshness there meant
the implementer had to run `make dashboard` and include assets in the same draft,
so source and bundle landed in the same commit. AGENTS.md already required this.
Landing was git-only. Adding npm/Vite there would have run workspace content in
the daemon outside a sandbox, contrary to AGENTS.md, and produced a commit that
neither reviewers nor QA had seen. The implementer's sandbox already permitted
dashboard builds.

The new check used prepared node_modules offline, loaded the existing Vite
config with the runner loader (avoiding a bundled config write under
node_modules), and overrode output and cache directories into a unique OS temp
directory. It compared every output file's bytes and relative path with assets,
including hidden and untracked files. It did not typecheck again. Missing,
extra and changed paths were reported, with the fix: run `make dashboard` and
commit internal/dashboard/assets. CI and release-check were left unchanged.

The read-only QA checkout needed no writes. Concurrent checks had separate
mkdtemp directories and only read the committed bundle. Normal errors removed
temporary output in finally. An interrupted build wrote only temporary files;
SIGKILL could leave that directory in OS/scratch temp storage, but left the
repository untouched and recorded nothing in it. Build errors were reported as
build failures, never staleness. Missing node_modules named prepared dependencies
as the fix. A stray .DS_Store was an extra file; rebuilding emptied assets.

Once this check landed, a sibling UI change without rebuilt assets would fail
the next check on any branch. The first landing after this one might need a
rebuild if such a change landed first (including CA-60's release settings).
After rebasing, the implementer was to rebuild a stale main bundle and mention
that in the handover. No daemon, landing, release, dependency or output-naming
changes were needed.

Implementation verification on 2026-10-03 used the sandbox with no network.
The supplied base bundle passed the new check. A temporary source statement
without rebuilding failed with missing/extra hashed files, differing index.html
and the `make dashboard` instruction; rebuilding passed. The statement was then
removed and the original bundle rebuilt. Two consecutive checks with UI files,
assets and the node_modules directory made non-writable both matched the bundle
byte for byte. The runner loader needed no config or cache write in the tree.
Eight fixture tests covered comparison, CLI results, cleanup and concurrency.

# Work test fixture maintenance follow-up, 2026-10-03

Base: the task’s draft 3, after merging main at `fc56d1e`; its preceding reviewed draft was `89e642a`, as recorded in the task. Measurements used the implementation sandbox on a local Darwin arm64 host with Go 1.27.1; they were not an owner-machine acceptance run. All fixtures were synthetic and offline.

This entry supplemented [the original timing record](2026-10-03-work-test-speed.md), preserving its before/after tables, baseline deviations and outliers.

## Copy race and fix

QA had reported a recursive copy failing because `.git/objects/maintenance.lock` disappeared. The starting repository was logically read-only after construction, but its first commit could start detached Git maintenance that continued after the command returned. A concurrent copy could enumerate its transient lock and then fail when that file disappeared. `sync.OnceValues` serialized construction, but did not wait for detached children.

The builder set local `maintenance.auto=false` and `gc.auto=0` before its first add and commit. This covered both modern automatic maintenance and the older automatic-GC path. Every copied repository inherited these settings, so subsequent synthetic commits also avoided detached maintenance. The source had only two consumers: copying and the isolation test. The latter’s status inspection also used `--no-optional-locks`, preventing optional index refresh from creating a transient `index.lock` during copies. The production adapter already disabled both automatic-maintenance paths and optional locks in its command environment; this brought the fixture into line with that policy. Production Git commands and repository configuration were unchanged; no retry or ignored copy error concealed the race.

The new regression test checked both settings in the shared source and a private copy. Removing the builder settings made that test fail at its local configuration lookup, then the settings were restored. The existing isolation test continued to verify independent content, HEAD and clean status. Fixture and exclusion tests passed 20 race-enabled repetitions (55.233 seconds under simultaneous full-check load); fixture-only tests also passed 20 race-enabled repetitions (11.984 seconds).

## Earlier review follow-ups

The shared-member test attempted a second scheduling pass inside A’s writer callback, while A still held the member. It required no new claim, then checked B’s recorded member wait and eventual completed draft. This exercised admission after the original scheduling pass without shortening an exclusion window or introducing a sleep.

`TestNoNewRoleTurnStartsWhileTheOwnerIsBusy` and `TestAQueuedChatHoldsRoleTurnsUntilItIsAnswered` became serial, with declaration comments explaining their real five-second grace assertions. Together with the original three serial tests, these covered the remaining wall-clock exclusion cases. No timing constant changed.

The earlier reviewer/QA duration increase from 1.47 to 5.01 seconds was measured in separate suite runs with different contention. The wake-channel wait introduced no five-second delay: it rechecked the recorded verdict after each wake and used a ten-second failure deadline. Contention was a plausible explanation, not a measured causal attribution; those samples could not establish a polling-to-signal slowdown.

## Validation after the fix

Main had added release and browser tests since the original 324-test count. Two initial package-only runs after the maintenance fix passed in 88.664 and 91.711 seconds (wall times 91.488 and 93.439 seconds); the latter missed the 90-second target. The first JSON run spent 34.08 seconds in serial release tests. These measurements justified parallelizing the 21 independent release tests and their three fixture-owning tables. Each case owned its Loop, runner, database, repository copies and fake remote; no case changed process-wide state or used a wall-clock exclusion window. Their assertions and production release behaviour stayed unchanged. The targeted release tests then passed with `-race` in 9.370 seconds. Browser tests newly added on main were retained unchanged. This follow-up also added one parallel maintenance regression test and made two grace-window tests serial. The original profile, shared-fixture isolation regression, parked-hook regression, timeout comment and production defaults were retained. No store option or duration injection was needed. The owner’s `TEST_TIMEOUT=30m` choice remained intact.

The release-parallel after table used package-only run 3 (the first run with parallel release tests). Per-test durations included parallel contention and were not additive; its start briefly overlapped the targeted release race check. The middle column used the first, maintenance-only follow-up run to show the additional effect of release parallelism. The original pre-change table remained in the linked timing record.

| Test | Maintenance-only (s) | Release-parallel (s) | Why it still cost time |
| --- | ---: | ---: | --- |
| `TestPMLandingStillBoundsRepeatedTargetMovement` | 15.73 | 14.98 | Repeated target commits, catch-ups and fresh checks up to the movement limit. |
| `TestDeliveredChangesLandOnMainInTheOrderTheyWereBuilt` | 16.57 | 14.57 | Two tasks with sequential git checks, catch-up and delivery. |
| `TestATargetThatKeepsMovingComesToTheOwner` | 11.48 | 10.22 | Repeated commits on main, catch-ups and rechecking. |
| `TestRepeatedLandingConflictsStayWithTheTeam` | 9.36 | 8.82 | Several real git conflict, revision and check cycles. |
| `TestTheOwnerApprovesAMergeThatIsReconsideredWhenThePullRequestChanges` | 7.50 | 7.47 | Multiple pushes, refreshed merge results and approvals. |
| `TestEachTaskGetsItsOwnCloneAndBranch` | 9.99 | 6.63 | Two task clones, snapshots, checks, landing and cleanup. |
| `TestThePMSendsAReadyPullRequestBack` | 7.18 | 6.60 | Git-backed PR revision and rechecking before PM delivery. |
| `TestTheSecondChangeCatchesUpWhenTheFirstLands` | 7.51 | 6.56 | Sequential task landings and catch-up of the second task. |
| `TestUnrecordedReplayKeepsTheOldBase` | 7.28 | 6.56 | Repository replay and restart reconciliation with real commits. |
| `TestAConflictWithWorkBuiltAfterGoesToTheImplementer` | 7.39 | 6.43 | Real conflicting commits followed by revision and checks. |

| Release sample | Maintenance-only (s) | Release-parallel (s) | Cause and change |
| --- | ---: | ---: | --- |
| `TestReleaseOwnerApprovalChecksAndPublishes` | 1.37 | 2.47 | Independent real git fixtures and release/check transitions became parallel; table parent durations excluded parallel child time. |
| `TestReleaseReconcilesPublishingAndChecking` | 5.10 | 0.00 | Independent real git fixtures and release/check transitions became parallel; table parent durations excluded parallel child time. |
| `TestReleasePauseAndMovingTarget` | 1.47 | 2.43 | Independent real git fixtures and release/check transitions became parallel; table parent durations excluded parallel child time. |

Five consecutive runs of the release-parallel implementation passed, before the broader helper fix described below. Runs 3–7 were consecutive package-only executions of the same tree; only the start of run 3 briefly overlapped the targeted release race check. No other check from this task competed with runs 4–7. The machine’s other workload was not controlled.

| Release-parallel consecutive run (scratch run) | Package (s) | Wall including build (s) |
| --- | ---: | ---: |
| 1 (3) | 54.239 | 57.150 |
| 2 (4) | 56.327 | 57.604 |
| 3 (5) | 55.905 | 57.018 |
| 4 (6) | 50.925 | 52.315 |
| 5 (7) | 64.303 | 65.656 |

All five met 90 seconds for both package and command wall time on the implementation host. Median package time was 55.905 seconds. No failure or copy race was observed. Acceptance timing on the owner’s Mac remained an owner check; sandbox measurements were not relabelled as owner-machine evidence.

After release parallelism, the tree contained 352 top-level tests: 341 parallel and 11 serial. Newly landed browser tests were left as they were; the original five timing-sensitive serial tests included the two grace tests above. Release table cases also ran in parallel. No test was removed, weakened, skipped or moved.

The first `make check` passed (work 84.623s; Go vet, all Go tests, frontend typecheck, bundle freshness and all 710 UI tests). The first `make test-race` passed (work 119.823s). Those preceded release parallelism and the final comprehensive commands were rerun. The final full checks and `-cpu 1,4` run were launched together after the five timings; their package times reflected competing validation load.

## Second housekeeping path found under validation load

The first concurrent final validation exposed the same failure in `TestSomeoneElsesPushToThePullRequestIsTakenInNotOverwritten`: local cloning of its fake bare remote failed on a disappearing `objects/maintenance.lock`. `make check` failed (work 191.588s); the accompanying `make test-race` passed (work 210.827s). These results were retained, rather than rerunning until the failure disappeared.

Bare remotes made with the general test Git helper did not inherit the starter’s local settings. A scratch push could therefore start receive-side housekeeping before a later clone copied the remote’s objects. The helper was changed to pass `maintenance.auto=false`, `gc.auto=0` and `receive.autogc=false` for every command, including the Git children of local pushes. It also disabled optional locks for all scratch inspections. The source builder still persisted its policy before the first commit, protecting copies independently of the helper. All other direct test Git invocations were inspected: they were read-only object/ref queries, ancestor checks and status inspections of private copies; writes used the protected helper. Production adapter commands remained untouched.

A second regression test created a fresh bare remote and checked the helper’s effective housekeeping settings. That covered the path that did not inherit source configuration. The final tree then had 353 top-level tests, 342 parallel and 11 serial. The five timings above preceded this helper change; five more consecutive runs and both full checks were required afterward.

The preceding `-cpu 1,4` command passed in 260.596 seconds under the three-command validation load. After the helper fix, source/copy, fresh-remote and pull-request clone tests passed 20 repetitions with `-race` in 87.446 seconds. Removing the helper’s command overrides made the new bare-remote regression test fail at its effective `maintenance.auto` lookup; the helper was restored immediately.

Final per-test durations came from the first package-only run after both housekeeping paths were fixed. The original pre-change table remained in the linked timing record; these after values included parallel contention.

| Slowest final tests | After (s) | Cause |
| --- | ---: | --- |
| `TestDeliveredChangesLandOnMainInTheOrderTheyWereBuilt` | 15.41 | Two tasks with sequential git checks, catch-up and delivery. |
| `TestPMLandingStillBoundsRepeatedTargetMovement` | 15.25 | Repeated target commits, catch-ups and fresh checks up to the movement limit. |
| `TestATargetThatKeepsMovingComesToTheOwner` | 10.88 | Repeated commits on main, catch-ups and rechecking. |
| `TestRepeatedLandingConflictsStayWithTheTeam` | 9.44 | Several real git conflict, revision and check cycles. |
| `TestTheOwnerApprovesAMergeThatIsReconsideredWhenThePullRequestChanges` | 7.25 | Multiple pushes, refreshed merge results and approvals. |
| `TestTheSecondChangeCatchesUpWhenTheFirstLands` | 7.11 | Sequential task landings and catch-up of the second task. |
| `TestThePMSendsAReadyPullRequestBack` | 7.09 | Git-backed PR revision and rechecking before PM delivery. |
| `TestAConflictWithWorkBuiltAfterGoesToTheImplementer` | 7.05 | Real conflicting commits followed by revision and checks. |
| `TestAStopWhilePushingRecordsTheChangeAsLanded` | 6.87 | Git push interrupted by stop, followed by reconciliation. |
| `TestUnrecordedReplayKeepsTheOldBase` | 6.64 | Repository replay and restart reconciliation with real commits. |

Five consecutive `go test ./internal/work -count=1 -json` runs of the final tree passed after both housekeeping fixes. No checks from this task competed with these five runs; other host workload was not controlled.

| Final consecutive run | Package (s) | Wall including build (s) |
| --- | ---: | ---: |
| 1 | 55.349 | 57.902 |
| 2 | 62.240 | 63.601 |
| 3 | 49.698 | 50.792 |
| 4 | 51.552 | 53.065 |
| 5 | 46.339 | 47.779 |

All five passed under 90 seconds, for package and wall time; median package time was 51.552 seconds. Both copy-race paths were exercised, and no flake or failure appeared in these runs. The final full checks and CPU-variation command were launched concurrently afterward, so their durations reflected competing validation load.

| Final full validation | Result |
| --- | --- |
| `make check` | Passed: Go vet, all Go packages, frontend typecheck, bundle freshness and 55 UI files / 710 UI tests; work 163.947s under concurrent validation load. |
| `make test-race` | Passed every package; work 184.345s under concurrent validation load, with no reported race. |
| `go test ./internal/work -count=1 -cpu 1,4 -json` | Passed both CPU settings; 233.136s combined under concurrent validation load. |

## Plan and acceptance audit

The original JSON/CPU/process-count profile, before tables and explained measurement omissions were retained in the original dated record. This follow-up added fresh per-test tables, all follow-up timings, the failed loaded check and the final successful checks. Expensive fixture reuse and its isolation regression remained; both source and remote housekeeping paths gained failing-without-fix tests. Signals kept bounded waits and positive-state assertions, and the shared-member test also exercised a second admission pass. Independent release cases added on main became parallel after measuring their serial cost. No new store option or injected duration was justified, and no test-runner race was reported. Production timing, state formats, Git sequences and defaults remained unchanged. The Makefile kept its background-load comment and `TEST_TIMEOUT=30m`.

Every in-sandbox acceptance check passed: final five consecutive package runs, race repetitions, CPU variation, `make check` and `make test-race`. Only confirmation of the under-90-second target on the owner’s Mac remained outside the implementation sandbox. The task-owned scratch logs were removed after recording the results; repeat the commands above to reproduce them with synthetic fixtures.

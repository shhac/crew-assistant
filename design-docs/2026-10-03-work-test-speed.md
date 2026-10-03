# Internal/work test timing, 2026-10-03

Base: `b1525f13f6aebe18e13e6f5c349dee746ef2c9bc`. These measurements described the native implementation sandbox on the local macOS arm64 host, with Go 1.27.1. The machine's other workload was not controlled. All repositories, state and runners were synthetic and offline.

The brief recorded 220 seconds on 2026-10-02 and approximately 320 seconds with CA-58's draft. CA-58's QA record gave 322.460 seconds. Its added git-backed planning tables helped explain the growth. CA-62 concerned sandboxed check execution, which this change did not touch.

## Measurements and diagnosis

The original package was measured with `go test ./internal/work -count=1 -json`, then with `-cpuprofile`. Package times were 350.643 and 361.560 seconds. Those runs overlapped; later profiling also competed with implementation checks, so they were load measurements rather than isolated benchmarks. The profiled original test binary was retained for a third, instrumented baseline run, which passed in 505.448 seconds while overlapping the full checks and CPU-variation run. A PATH wrapper counted one record per git process, then executed the real local git binary. No git command sequence in the production adapter was changed.

The unchanged binary invoked git 13,954 times; the first consecutive optimized run invoked it 13,518 times. Initializations fell from 178 to 30, adds from 178 to 31, and commits from 183 to 36, including the new isolation test's extra commit. Adapter invocations prefixed with `-c` stayed exactly at 13,119. Fixture reuse removed setup processes; parallelism overlapped the remaining work. The medium's real git operations were still exercised.

The CPU profile covered 361.19 seconds but sampled only 38.66 CPU seconds (10.70% of elapsed time). `syscall.rawsyscalln` accounted for 16.75 seconds flat; `runtime.pthread_cond_wait` for 8.83. SQLite's VDBE accounted for 3.32 seconds cumulatively and JSON unmarshalling for 1.06. CPU samples excluded child git processes and could not measure their wall time. Per-test durations and the process count were the stronger evidence for sequential git work, repository setup, copies and filesystem cleanup. The results did not justify a new SQLite durability option, so the original disk-backed store and its sync policy remained in every test.

`-p 1` was not repeated three times: it controls concurrency between packages, and these baseline commands tested exactly one package with no parallel tests. Two JSON baselines and a third run of the unchanged profiled binary supplied the before evidence instead. A separate original `-race` baseline was not collected; race validation concentrated on the changed concurrent suite and the full repository. These were deviations from the measurement plan, not missing tests from the check targets.

The before column used the profiled JSON baseline. The after column used the second consecutive package-only run, without the counting wrapper. Values came from per-test pass-event `Elapsed`, rounded to two decimals. Parallel tests competed for CPU and disk, so individual durations sometimes increased while total package time fell. The Go runner excluded parallel-child time from parent durations: a zero parent duration did not mean its cases were free or omitted. Those rows also showed the slowest child. Durations were not additive.

| Test | Before (s) | After (s) | Cause and change |
| --- | ---: | ---: | --- |
| `TestFeatureSummariesResearchOnce` | 25.55 | Parent 0.00; slowest case 4.94 | Repeated git-backed planning fixtures in a serial table; cases became parallel and copied the shared starter. |
| `TestPMLandingStillBoundsRepeatedTargetMovement` | 17.60 | 15.73 | Repeated target commits, catch-ups and fresh checks up to the movement limit; independent test became parallel. |
| `TestARestartMidHandoffNeitherLosesNorRepeatsADraft` | 16.63 | Parent 0.00; slowest case 6.40 | Six independent repository/restart/replay scenarios; cases became parallel and reused the starter. |
| `TestRepeatedLandingConflictsStayWithTheTeam` | 11.45 | 9.61 | Several real git conflict, revision and check cycles; independent test became parallel. |
| `TestAnsweredWaitQuestionIsNotAskedAgain` | 10.44 | Parent 0.00; slowest case 5.51 | Four independent answer/fallback cases, each setting up a code project; cases became parallel and reused the starter. |
| `TestTheOwnerApprovesAMergeThatIsReconsideredWhenThePullRequestChanges` | 8.24 | 7.01 | Multiple pushes, refreshed merge results and approvals; independent test became parallel. |
| `TestDeliveredChangesLandOnMainInTheOrderTheyWereBuilt` | 8.15 | 29.98 | Two code tasks with sequential git checks, catch-up and delivery; independent test became parallel. |
| `TestEachTaskGetsItsOwnCloneAndBranch` | 7.19 | 7.67 | Two task clones, snapshots, checks, landing and cleanup; independent test became parallel. |
| `TestThePMSendsAReadyPullRequestBack` | 7.12 | 7.39 | Git-backed PR revision and rechecking before PM delivery; independent test became parallel. |
| `TestAFailedCheckOnTheMergedResultGoesBackThenToTheOwner` | 6.82 | 5.80 | Repeated merged-result checks and revisions; independent test became parallel. |
| `TestThePMIsNotAskedAboutAChangeThatIsNotSignedOff` | 6.34 | 5.24 | Git-backed failed review and revision rounds; independent test became parallel. |
| `TestAStopWhilePushingRecordsTheChangeAsLanded` | 6.15 | 7.15 | Git push interrupted by stop, followed by reconciliation; independent test became parallel. |
| `TestATargetThatKeepsMovingComesToTheOwner` | 5.85 | 18.71 | Repeated commits on main, catch-ups and rechecking; independent test became parallel. |
| `TestWhatAPlanSplitsOffIsQueuedToWaitForTheTask` | 4.69 | 3.78 | Git-backed planning and split-task setup; independent test became parallel. |
| `TestAChecksCopyHoldsStillWhileTheImplementerWorksOn` | 4.67 | 4.87 | Separate real check copies while another revision writes; independent test became parallel, retaining its signals. |

The slowest after tests in that same run were:

| Test | After (s) |
| --- | ---: |
| `TestDeliveredChangesLandOnMainInTheOrderTheyWereBuilt` | 29.98 |
| `TestATargetThatKeepsMovingComesToTheOwner` | 18.71 |
| `TestUnrecordedReplayKeepsTheOldBase` | 18.32 |
| `TestPMLandingStillBoundsRepeatedTargetMovement` | 15.73 |
| `TestAConflictWithWorkBuiltAfterGoesToTheImplementer` | 15.70 |
| `TestARewrittenDescriptionUpdatesTheOpenPullRequest` | 14.57 |
| `TestAConflictWithARewrittenMainReachesTheImplementer` | 14.51 |
| `TestLeftoverConflictMarkersRetryWithNamedDecision` | 13.73 |
| `TestAConflictBetweenTasksBuiltSideBySideGoesToTheImplementer` | 13.64 |
| `TestPullRequestFeedbackNamesTheCommitItWasOn` | 13.17 |

The replaced sleeps and polls were measured separately:

| Test | Before (s) | After (s) |
| --- | ---: | ---: |
| `TestAMemberSharedByTwoProjectsWorksOneStepAtATime` | 0.14 | 0.05 |
| `TestAClaimedStepWaitsForChatThatBeganAfterItWasClaimed` | 0.31 | 0.02 |
| `TestReviewerAndQACheckOneDraftAtOnce` | 1.47 | 5.01 |
| `TestUsageIsBoundedAndNeverSpendsALook` | 0.04 | 0.04 |
| `TestARefusalHasTheLoginReadAgain` | 0.00 | 0.04 |

## Changes that landed in the draft

- 321 of 324 top-level tests ran with `t.Parallel()`. The planning tables and restart-handoff cases also ran their independent fixtures in parallel. Each case retained its own store, loop, runner and workspace. All existing assertions remained.
- The starting owner repository was built once with `sync.OnceValues`, inside a temporary root made before `m.Run`. Each caller copied it into its own test directory, with `cp -Rc` on macOS and `cp -R` elsewhere. The production git adapter remained unchanged. A construction error was cached and returned to every caller; TestMain removed the root after passing or failing runs.
- The shared-member test read B's recorded member wait while A held the seat. Schedule had already recorded that wait before launching A, so the positive assertion replaced its 100ms sleep without another hook.
- The claimed-turn test waited for the new optional `parked` callback from `waitQuiet`, after its wake channel had been registered. It asserted that no turn had started while the composer was active, released the composer, then asserted exactly one turn. The hook was nil in production and changed no timing constant.
- The reviewer/QA test waited on the loop's existing wake channel and checked the recorded verdict after each wake, instead of polling every 10ms. Its original overlap and intermediate-state assertions remained, and a missing verdict had an explicit bounded failure.
- Usage tests joined the inspection already in flight through the meter's existing serialized `Observe`, then asserted that the cached reading governed admission. Both counted inspections and required exactly one, so the join could not mask a missing cached reading by starting a fresh inspection. The two 5ms polling loops disappeared.
- `TEST_TIMEOUT` stayed at 30m. Only its comment changed, describing the margin for background-priority checks beside other work.

No production timing field or store option was added: measured git costs dominated, and existing signals sufficed. `interactiveGrace`, admission fallback timers, streaming intervals, wake intervals, seat retry times and the Run ticker retained their defaults. No state format, daemon scheduling, delivery behaviour or git command sequence changed.

## Exclusion windows, signals and failure behaviour

Three tests stayed serial, with comments at their declarations:

| Test | Reason |
| --- | --- |
| `TestTurnsOnOneEngineWaitForAFreeSlot` | Retained a 50ms overlap window in each check to exercise engine exclusion; a serial test avoided load making that negative window vacuous. |
| `TestUsageIsBoundedAndNeverSpendsALook` | Measured a real response-time bound; the asynchronous result and exactly one inspection were still asserted. |
| `TestNoSleepIsSeenWhileAwake` | Measured the real sleep observation against a one-second wall-clock bound. |

The other exclusion tests either synchronously attempted admission or paired a negative assertion with a positive reached-state signal. Signal waits retained bounded deadlines: 10 seconds for a parked turn or reviewer verdict, two seconds for a usage inspection. A lost signal failed an assertion rather than consuming the package's 30-minute ceiling. Existing concurrency barriers retained their deadlines.

Process-wide git environment variables were set only in TestMain, before tests ran. The shared repository was read-only after construction; commits happened only in copies. No runner or production data race was reported by the race checks.

The regression checks were also run with deliberate mutations, restored immediately afterward. Removing the `parked` callback failed `TestAClaimedStepWaitsForChatThatBeganAfterItWasClaimed` with "the claimed turn never waited for the composer" after 10 seconds. Returning the shared repository directly instead of copying failed `TestOwnerRepositoryCopiesAreIsolated` with "copy changed another repository". The isolation test changed and committed an existing tracked file in one copy, then checked the second copy and source's content, HEAD and clean status. These checks demonstrated failures at the intended assertions, not compilation errors.

## Validation

Five consecutive `go test ./internal/work -count=1 -json` runs passed on the final test implementation, with no competing checks from this task. The first used the transparent git-counting wrapper; the other four used the ordinary PATH. The machine's other workload was not controlled. No test failure or race was observed.

| Run | Package time (s) | Command wall time including build (s) |
| --- | ---: | ---: |
| 1, counting wrapper | 71.112 | 73.611 |
| 2 | 77.815 | 79.413 |
| 3 | 95.324 | 96.939 |
| 4 | 64.300 | 65.795 |
| 5 | 57.239 | 58.791 |

The median was 71.112 seconds, versus the original 350.643 and 361.560-second loaded runs. Four of five runs met the 90-second target; the third did not. This set alone did not establish five consecutive sub-90-second timings. The result was recorded rather than discarded or described as five sub-90-second runs.

A lower-concurrency experiment, `go test ./internal/work -count=1 -parallel=4 -json`, passed in 97.968 seconds. It was slower than the default-concurrency median, so no concurrency cap was imposed in TestMain or the Makefile. A second consecutive set, without the counting wrapper, followed to check the timing variance.

| Second consecutive set | Package time (s) | Command wall time including build (s) |
| --- | ---: | ---: |
| 1 | 59.352 | 60.789 |
| 2 | 53.411 | 55.128 |
| 3 | 56.812 | 58.562 |
| 4 | 56.144 | 57.648 |
| 5 | 50.576 | 51.785 |

All five passed under 90 seconds on the local macOS host, including command wall time. The second set's median package time was 56.144 seconds. Both sets were kept in the record: the earlier 95-second outlier and the loaded full-check timings explained why the owner retained the generous 30-minute ceiling.

| Validation | Result |
| --- | --- |
| Initial optimized package `-race -count=1 -json` | Passed, 99.126s while baselines also ran |
| `go test ./internal/work -count=1 -cpu 1,4 -json` | Passed, 188.717s for both CPU settings together, while other checks ran |
| Signal, member-wait, usage and fixture tests, `-race -count=20` | All six tests passed all 20 repetitions, 91.830s total under concurrent check load |
| `make check` | Passed twice: Go vet, every Go package, frontend check, 52 UI files and 693 UI tests. Work package times were 113.786s and 127.305s under competing checks. |
| `make test-race` | Passed every package; work took 164.064s while the instrumented run and other packages also used the machine. |
| Deliberately omitted parking callback | Regression test failed its bounded positive-state assertion |
| Deliberately returned shared source without copying | Regression test failed its isolation assertion |

Parallelization was checked with whole-package race runs, rather than separate race runs after each one-line file edit. The later exactly-one-inspection assertion was also exercised in the 20-repeat targeted race check and every consecutive package run. The team's check command, test selection and timeout value were unchanged; no tests were removed, skipped or moved.

To reproduce, run the package five times with `-count=1 -json`, sort each test's pass-event `Elapsed`, and run `make check`, `make test-race`, and the `-cpu 1,4` package command. For CPU diagnosis, add `-cpuprofile` and inspect it with `go tool pprof -top`; remember that it does not profile git children. For process counts, put a temporary wrapper on PATH that appends exactly one line per invocation (not the possibly multiline arguments) and then execs the real git binary. The scratch logs, profile, binary and mutation scripts were removed after these tables were recorded.

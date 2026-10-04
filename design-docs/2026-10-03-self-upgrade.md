# Self-upgrade, 2026-10-03

Version: nothing to pin, code-internal. This snapshot recorded the self-upgrade
implementation after update notices and two-stage stopping had landed.

Off and Ask remained available in the dashboard. Automatic was added to config
and CLI completion; its dashboard option and progress/banner design were left
to the agreed follow-up. Ask offered Upgrade to vX only with an injected
Homebrew capability. Explicit choices invoked the same engine as Automatic.
Custom text spelling a choice did not invoke it. Failures opened an upgrade-failed
decision once per attempt, with Stay and Try again choices. The external record
and a transaction key made that opening repeatable across crashes.

Automatic checked each minute for a quiet moment with no live role turn or chat
reply. It waited at most six hours before draining. Admission stopped through
the existing lifecycle Stop, while work retained Force. Signals took precedence
over upgrade draining. Activity and the state API recorded what the drain was
waiting on; queued work did not count as running work or continue dispatching.

The upgrade record lived beside SQLite, outside the data a rollback replaced.
Each step was written atomically and synced before its effects. It recorded
the versions, executable and backup paths, timestamps, launchd label, serve
arguments, PID, address, probation deadline, failure and rollback pin. An
existing pin and its binary/backups were retained while a retry drained and
backed up, so a crash could not silently clear a rollback.

Backup used VACUUM INTO while SQLite was open, including committed WAL pages.
The dashboard stayed available through installation; config and the running executable
were copied privately (0600 and 0700). Homebrew's prefix was resolved from the
Cellar executable path. Installation used its absolute bin/brew and removed
HOMEBREW_NO_INSTALL_CLEANUP. Version probing ran even after brew failed. If a
newer version had appeared meanwhile, the record followed the installed version.
Unlike the original plan's stderr tail, arbitrary installer output was excluded
from errors because user hooks could print credentials. Exit status and manual
retry guidance supplied safe diagnostics.

Handover duplicated the TCP listener, kept close-on-exec until the replacement syscall, and passed its
descriptor through CREW_UPGRADE_LISTEN_FD. The receiver consumed and unset that
variable immediately. Private browser-session handover retained cookie expiry
while rotating the admin token. The Tailscale route stayed in place across exec.

Every real serve start took the state lock and ran recovery before config
migration or SQLite open. Interrupted draining/backups became abandoned for any
running version and their partial files were discarded. Later unexpected
versions restored and pinned the previous version. Installing on the old
version became an install failure; on the new version it entered probation.
Probation was journaled before migration, including its attempt count. A second
probation start restored the previous version, including after an owner stop.
The new process opened state and answered its authenticated API through the
listener within a two-minute deadline. No work loops ran until success, and
mutations received 503 upgrading (duplicate upgrade requests received 409).

Restore streamed and synced both backups to temporary files before replacing
either destination, removed WAL/SHM files, and synced directory entries. Store
and HTTP close was idempotent. A rolling-back record repeated restore before
opening any state, so the old daemon could not read the new version's state.
Healthy retained only the latest attempt's backups.

A detached watchdog ran from the saved binary before handover and on probation
startup, covering a lost watchdog and interrupted installs. It journaled
rollback before killing a timed-out PID, took the same state lock after death,
and restored before restarting. Terminal recovery started the previous serve
detached with rollback-serve.log and recorded the reason. Launchd recovery used
a label recorded only for a process parented by launchd (excluding 0 and application.*) with kickstart; Homebrew's binary honored the pin
and execed the saved one. Terminal starts named an active rollback and its log,
even when the detached daemon already owned the state lock.

Upgrade status read the record with no daemon. Clear-rollback asked the daemon
to take fresh backups and retry, retaining its pin until safe handover. A narrow
offline rescue for a missing saved binary restored backups and removed the
unusable pin without starting anything; it required exclusive state ownership
and refused while the saved binary existed.

Tests used synthetic SQLite/config files, fake installers and binaries,
injected clocks and process effects, HTTP recorders, and blocked role/chat turns.
Listener re-exec and live serve tests skipped only EPERM on bind; setting
CREW_REQUIRE_UPGRADE_LOOPBACK=1 made that refusal a failure. They did not run
Homebrew, launchctl, real agents, or owner state.

Recovery corrections: journals were namespaced by the absolute state filename
and bound to the config destination. Newly created backup directories were
synced in their parents. Journal transitions and watchdog claims shared a file
lock; duplicate watchdog runners shared an exclusive helper lock. Restart
remained pending until the restored daemon acknowledged startup, so failed
starts could be retried. Healthy starts reconciled interrupted backup pruning.
Session handover remained durable throughout probation. The installed binary's
version probe had its own ten-second bound. First signals finished installation;
only Force cancelled it. Backup failure restarted the running executable and
reported the failure. An unchanged formula restarted the saved version without
introducing a new rollback pin. A second probation start always rolled back,
including after an owner stop: this was the chosen safe reading of the retry
rule, rather than allowing an additional migration after a stop.

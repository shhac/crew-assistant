# Update notices and the installation split

Date: 2026-10-03. Version: nothing to pin, code-internal.

As of this change, the owner had agreed to split noticing releases from installing them. Update settings offered off and ask only, with ask as the default. A release was offered only after its Homebrew formula included the version; GitHub supplied that version's notes. Configurable repository, formula and URL overrides allowed synthetic sources in tests. Checks ran every six hours by default, immediately on start, and every two minutes for at most one hour after a matching project published a newer release. A recorded release woke the checker only after the state transaction committed.

Checks and upgrade-available decisions were saved in one transaction. Errors retained the previous result and open decision, with a bounded error and check date. A skipped or already answered version was not asked about again. A newer version superseded an open decision, even with notices off; off never opened a new decision. Closed decisions rejected late answers. Startup reconciled the running version with the stored available version even when the source was offline. Development and demo builds made no requests.

The dashboard reused its existing sidebar text and settings panel patterns, showing running and available versions and manual instructions. There was no Upgrade button, automatic option or installer.

## Agreed follow-up

Installation, automatic mode and an Upgrade choice were deferred. The agreed design drained active work before Homebrew installation, then handed over to the new binary while keeping the dashboard address and paired browser. A durable record would precede each step. The previous executable would be copied independently of Homebrew's old keg, alongside state and config backups, so later cleanup could not remove rollback's binary.

The new process would enter probation with dispatch held until state opened and migrated and the API proved healthy within a bound. Failed probation or a crash would restore the previous executable, state and configuration; a watchdog would cover a process that could no longer recover itself. Restoring SQLite state would remove its WAL and SHM companions.

Both terminal and launchd starts were in the follow-up's scope. While rollback was in force under launchd, the installed binary would hand off to the saved previous one. The dashboard and engine-status-style CLI output would state plainly that rollback was in force and how the owner could clear it. HOMEBREW_NO_INSTALL_CLEANUP would never be set. Upgrade progress and the full operating instructions also belonged to that follow-up.

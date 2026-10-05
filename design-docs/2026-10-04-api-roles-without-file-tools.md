# API roles without workbench content tools

As of 2026-10-04, with published lib-agent-harness v0.24.0 (including the
command execution policy prepared as v0.23.4, which was not tagged).

This superseded [API team roles](2026-10-02-api-team-roles.md)'s file-tool
availability. On macOS and Linux the combined WorkspaceRead and WorkspaceWrite
claims were Unsupported: “workbench file tools are off until their workspace
check is verified”. Roles remained eligible only with Sandbox and Tools usable.
Their command boundary still had to be proved at session open. A missing or
failed command sandbox permanently refused launch, before opening or recording
a transcript; there was no native or unsandboxed fallback.

Roles read, searched and edited through run_command. list_files remained
available; writing turns retained write_file. read_file, search_files and
edit_file were absent. The workbench dispatcher returned not_offered; the
HTTP completion parser rejected forced absent names earlier with invalid_tool_call,
ending that turn without reaching caller handlers. Each session's
Capabilities.WorkbenchTools report supplied the
names and reasons persisted with its existing opening write and shown in turn
history. Old records decoded without a report; repeated openings stayed
idempotent. Caller task tools, permissions, browser policy and caller-only
assistant chat were unchanged.

Start, Open and Resume remained available. The library's workbench reference
digest remained compatible. Crew's updated API instruction text changed its
instruction hash, so existing API role threads opened fresh once with task
context and harness_incompatible recorded. CLI instruction text was unchanged.
A failed fresh open retained the previous reference.

Commands filtered PATH against the read policy and refused executables outside
it. Git workspaces granted their existing module cache plus resolved Go GOROOT
and dedicated Node install directories, for command sandboxes and CLI role
sandboxes alike. Node roots required include/node or lib/node_modules/npm;
shared prefixes were refused. Only existing absolute directories that did not
contain home or Crew default/XDG config/state paths were granted; lookup failures granted nothing.
The containment guard did not identify a custom daemon --state location;
such a location needed to remain outside the named toolchain directories.
Home-installed toolchains could be granted narrowly. Xcode, CommandLineTools and
Homebrew were already covered by the harness's macOS system read set. No arbitrary
home folder or additional toolchain grant was added. CommandEnv's filtering
remained in place.

A Start error after launch could include a settled handle. App hosting consumed
its result and retained bounded stderr, including the harness PATH note, before
returning the failure with no live handle. run_check and release checks kept
their existing Run settlement, CA-92 required-skip enforcement and CA-113
identifying coverage evidence.

## Release note

Adopted lib-agent-harness v0.24.0 without a local replacement. API team roles
continued through proved commands while unavailable content tools and their
reasons became visible in doctor and turn details. Checks and app commands gained
named toolchain reads and retained post-launch PATH diagnostics. Restoration of
the three file tools remained upstream work (LAH-27/28).

## Validation record

The published module checksum matched the cached v0.24.0 zip checksum:
`h1:pY2xk2tX3Tgq681rzfU21kv6TStE7EkG0Y3O+n6GkDA=`.

The owner validated revision ba5a1b7 outside the sandbox on darwin/arm64,
macOS 27.0 (26A428), with published lib-agent-harness v0.24.0.
`make check` exited 0 with terminal stage complete; `make test-race` exited 0.
The complete check report was:

`Go check skip count: 1; required-skip failures: 0; complete: true`.

The sole skip was `internal/cli TestGeneratedShellCompletionSyntax/fish`:
`completion_test.go:142: script generated; fish unavailable for syntax check`
(optional Fish syntax validation). Process-inspection tests ran outside the
sandbox, so no LAH-29 exception was needed. TestAPICommandProof,
TestCommandBackedAPIStartsWithFileToolsAbsent and
TestGrantedToolchainCommandSandbox passed their success paths.
This was the owner-accepted validation of that revision; a later main merge
required separate validation of the resulting revision. CA-92/CA-113
enforcement and known-skip policy were unchanged.

### Post-merge confirmation, 2026-10-05

The merged draft retained the command-proof prompt, tool-absence startup
regression, private runtime-home toolchain fixture, settled-start diagnostics
and live-sandbox recovery tests. Focused regressions passed under the race
detector across config, roles, gitrepo, core and work. Vet, frontend types,
bundle freshness and all 799 UI tests passed separately on darwin/arm64,
macOS 27.0 (26A428), with published lib-agent-harness v0.24.0.

The full hosted `make check` timed out in Go tests: `exit_code=-1`,
`timed_out=true`, `truncated=false`. Its recovered report was:

`Go check skip count: 1; required-skip failures: 0; complete: false`.

The sole observed skip remained optional Fish syntax validation:
`internal/cli TestGeneratedShellCompletionSyntax/fish`,
`completion_test.go:142: script generated; fish unavailable for syntax check`.
This partial report did not establish complete coverage of the merged draft.
The owner treated hosted timeouts as an environment limit, accepted the
outside-sandbox ba5a1b7 evidence above, and assigned both outside-sandbox suites
on the new recorded revision to the owner after landing. No checker policy,
known-skip entry, sandbox permission or check recipe changed.

### Review rerun, 2026-10-05

A further normal daemon-hosted `make check` settled with `exit_code=-1`,
`timed_out=true`, `truncated=false`, terminal stage Go tests. Its complete
available skip report was:

`Go check skip count: 1; required-skip failures: 0; complete: false`.

The sole observed skip was `internal/cli TestGeneratedShellCompletionSyntax/fish`:
`completion_test.go:142: script generated; fish unavailable for syntax check`
(optional Fish syntax validation). This run did not establish full-suite
success. The owner-accepted outside-sandbox validation above remained distinct
from this incomplete hosted confirmation; both suites on the new recorded
revision remained assigned to the owner after landing. No implementation,
permissions or CA-92/CA-113 enforcement changed for this rerun.

# API team roles

As of 2026-10-02, with lib-agent-harness v0.20.0.

Team members could select a named OpenAI-compatible provider where the harness
claimed a sandboxed Workbench session: macOS and Linux in this release. Other platforms
showed the harness's reason. Each role received workspace read tools and the
daemon's existing task tools; only a writing turn received file writes.
Commands used the role's read directories and existing loopback policy. A failed
pre-launch proof stopped at one failure decision, without retry or a weaker
session. Changed session settings opened a fresh conversation with task context.
A non-secret provider ID and credential-source name distinguished providers
sharing an endpoint; credential values never entered a reference.

The API transcript required a private runtime directory. It did not use a CLI
login home, browser, web search or the CLI background-priority option; the harness
lowered its own commands. External read directories were accessible through
commands, rather than through workspace file tools.

Commands.Env carried the medium's build settings, including its Go and npm
caches, offline Go proxy and local toolchain settings, plus QA's PORT when running
the app. Only PATH and locale settings were inherited from the daemon. HOME and
TMPDIR remained private harness scratch; other caller settings reached the
harness's validation whole, so unsafe names failed before launch rather than
being silently dropped. No environment-setting workaround was added to API role
instructions.

API QA could run the app on macOS with its reserved port and loopback policy.
Linux offered commands under bubblewrap, but the harness refused loopback across
separate commands because each command had its own network namespace. API QA app
runs there were marked unavailable with that reason; checks still ran. Browser
use remained unavailable for API roles on every platform.

The owner's live provider implementation-to-landing check remained an owner
check after landing.

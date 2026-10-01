# Task Linear links — 2026-10-01

Version: CA-52; nothing to pin: code-internal.

Tasks gained owner-added Linear issue links and one replaceable Linear project link, separate from imported source issues. Both the dashboard and assistant tools checked metadata through fixed read-only lin queries, using the project's connection by default or another configured account. Stored metadata included the issue identifier or project name, title, URL, account, setter and timestamp; descriptions were not added to team prompts.

Issue links also recorded an import receipt in the same transaction. Removing a link retained that receipt, so pick-up could not create a duplicate task later. A concurrent import that completed before linking remained intact. Duplicate issue adds wrote nothing, project links replaced the preceding project, and source links remained read-only. A task admitted up to 50 added issue links.

Failed reads persisted nothing and never echoed CLI output. Connection validation ran again in the store transaction, and links were refused while stopping. No operation wrote to Linear. The task panel reused existing controls and external-link styles; board cards continued to show source issues only.

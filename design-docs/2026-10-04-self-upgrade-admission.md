# Self-upgrade admission and inspection — 2026-10-04

Version pin: nothing to pin: code-internal. This snapshot described the final
admission and process-inspection corrections after the recovery-boundaries
snapshot, against the merged main at 3c7009d.

Automatic admission rechecked the available version, skipped/failed markers,
mode and running work while holding core's state and configuration locks. That
same boundary journaled the drain and closed new role, PM, release and chat
claims. A claimed operation counted as busy even before its model observer
started. Six hours overrode busy work, but never an ineligible or superseded
candidate. Explicit requests used the same admission boundary; already admitted
work could finish and release its claims normally.

Receiver startup and upgrade requests refused to publish an empty process
identity. The watchdog distinguished a matching process from a reused PID and
an unknown identity. It revalidated a matching identity immediately before
killing, and never converted a mismatch or failed lookup into permission to
kill. Unknown inspection produced a fixed, rate-limited diagnostic and left
recovery pending, including beyond the health deadline.

Healthy records and unpinned, nonpending abandoned records no longer restricted
the configuration path on ordinary starts. Active recovery and retained pins
continued validating the recorded destination before restoration. The example
configuration described Automatic and its quiet-minute/six-hour behavior.

Regression tests covered superseded, skipped and disabled automatic candidates,
newly claimed chats and PM work, concurrent claim admission, the six-hour
override, missing startup identity, PID mismatch followed by inspection failure,
persistent unknown inspection and changed configuration after completion.
Dashboard progress, banners and the Automatic control remained deferred to the
agreed separate dashboard task.

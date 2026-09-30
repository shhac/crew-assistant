# External blockers — 2026-09-30

Version: nothing to pin: code-internal.

As of this change, requests could wait on external conditions alongside task
relations. Conditions either held the start and landing, or landing alone.
Cleared conditions stayed on the request as history. Team-set conditions could
be cleared by the team; owner and assistant conditions held against it.

A manual condition was cleared from the request panel or by the PM. A daemon
condition named a request in the same code project, and cleared automatically
when the running build's recorded commit contained its landed revision or the
exact trailer of its squashed landing. The check read the local source repository
without fetching. A missing build stamp, object or removed request left the
condition open with a short reason; git errors were retried at most once a minute.
The owner could clear such a condition manually, without opening a decision.

Changes were atomic with activity records. A restart retained open and cleared
conditions but discarded the observation cache. Automatic clearing checked the
current target and revision again inside the store update. A concurrent manual
clear won or lost that serialized update, recording one clearing author. Delivery
already begun finished; a newly added condition remained on the finished record.
A new daemon process checked open conditions against its own build, and kept
cleared history unchanged. No daemon restart, build or deployment was requested.

Assistant tools and other automatic kinds remained outside this change.

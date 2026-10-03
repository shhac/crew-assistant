# Update notice recovery after review

Date: 2026-10-03. Version: nothing to pin, code-internal.

As of this revision, update checks continued after a failed state write. Each failure produced a secret-safe diagnostic and a live dashboard error, since an unavailable store could not reliably save its own error. The next timer or release nudge retried, and a successful save cleared the live error. Startup reconciliation remained pending until it succeeded, including when the HTTP source was unavailable. Cancellation ended checks without logging a retry or writing a result.

Only changes to the upgrade settings woke the checker; appearance, model and other configuration edits left the polling schedule alone. A newer release resolved the older notice as superseded. Reaching the available version resolved its notice as completed, with a reason and no invented owner answer. These automatic closures were distinct from an owner's dismissal and did not skip a version.

The sidebar linked an available release to its open decision in the inbox, scrolling and focusing that card. Without an open decision it linked to Updates in Settings. The bundle was regenerated from the merged source, retaining the newly landed bundle-freshness check.

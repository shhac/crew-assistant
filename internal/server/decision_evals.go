package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
)

// Finish reading before sending headers so a failed export cannot look like
// a successful, truncated JSONL download.
func decisionEvaluationHandler(export func(context.Context, io.Writer) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body bytes.Buffer
		if err := export(r.Context(), &body); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", `attachment; filename="decision-evaluations.jsonl"`)
		_, _ = w.Write(body.Bytes())
	}
}

package server

import (
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/shhac/crew-assistant/internal/app"
)

func registerArtifacts(mux *http.ServeMux, a *app.App) {
	// The path segment after the token is a download name only. It never takes
	// part in resolution, so it cannot be used to reach another file.
	mux.HandleFunc("GET /api/artifacts/{token}/{name}", func(w http.ResponseWriter, r *http.Request) {
		s, err := a.Snapshot(r.Context())
		if err != nil {
			problem(w, err)
			return
		}
		name, file, size, err := a.OpenArtifact(s, r.PathValue("token"))
		if err != nil {
			fail(w, 404, "artifact not found")
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
		w.WriteHeader(200)
		_, _ = io.Copy(w, file)
	})
}

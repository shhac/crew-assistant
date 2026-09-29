package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/shhac/crew-assistant/internal/config"
)

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "invalid request body")
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		fail(w, 400, "request must contain one JSON object")
		return errors.New("trailing JSON")
	}
	return nil
}

// decodeConfig reads a config in any layout: a dashboard left open across an
// upgrade still sends the one it loaded.
func decodeConfig(w http.ResponseWriter, r *http.Request, c *config.Config) error {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err == nil {
		data, err = config.ConvertLegacyJSON(data)
	}
	if err != nil {
		fail(w, 400, "invalid request body")
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	return decode(w, r, c)
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}

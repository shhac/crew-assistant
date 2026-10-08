package cli

import (
	"net/http"
	"strings"
	"testing"
)

// replyingDaemon answers every request with status and body, where the CLI
// looks for a running daemon.
func replyingDaemon(t *testing.T, status int, body string) *options {
	t.Helper()
	return standIn(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}), "token")
}

func TestRequestReportsTheDaemonsRefusal(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"its message", 409, `{"error":"already running"}`, "already running"},
		{"a message without text", 400, `{"detail":"x"}`, "<nil>"},
		{"a reply that is not an object", 500, `["x"]`, "daemon returned HTTP 500"},
		{"a plain-text page", 404, "404 page not found\n", "daemon returned HTTP 404"},
		{"an empty reply", 502, "", "EOF"},
		{"an unreadable reply", 500, "<html>", "invalid character '<' looking for beginning of value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := replyingDaemon(t, test.status, test.body)
			if _, err := o.request("GET", "/api/state", nil); err == nil || err.Error() != test.want {
				t.Fatalf("request: %v", err)
			}
			var out struct{}
			if err := o.requestInto("GET", "/api/state", nil, &out); err == nil || err.Error() != test.want {
				t.Fatalf("requestInto: %v", err)
			}
		})
	}
}

// paddedState is a state reply padded with size bytes of activity.
func paddedState(size int) string {
	return `{"paused":true,"activity":["` + strings.Repeat("x", size) + `"]}`
}

func TestRequestReadsAStateLargerThanTheOldLimit(t *testing.T) {
	o := replyingDaemon(t, 200, paddedState(14<<20))
	var out struct {
		Paused bool `json:"paused"`
	}
	if err := o.requestInto("GET", "/api/state", nil, &out); err != nil || !out.Paused {
		t.Fatal(out, err)
	}
}

func TestRequestNamesTheLimitWhenAReplyOutgrowsIt(t *testing.T) {
	o := replyingDaemon(t, 200, paddedState(maxReplyBytes))
	if _, err := o.request("GET", "/api/state", nil); err == nil || err.Error() != "the daemon's reply is larger than 128 MiB" {
		t.Fatal(err)
	}
}

func TestRequestDecodesTheDaemonsAnswer(t *testing.T) {
	o := replyingDaemon(t, 200, `{"paused":true,"count":3}`)
	v, err := o.request("GET", "/api/state", nil)
	if err != nil || v.(map[string]any)["count"] != float64(3) {
		t.Fatal(v, err)
	}
	var out struct {
		Paused bool `json:"paused"`
		Count  int  `json:"count"`
	}
	if err := o.requestInto("GET", "/api/state", nil, &out); err != nil || !out.Paused || out.Count != 3 {
		t.Fatal(out, err)
	}
	var wrong struct {
		Paused string `json:"paused"`
	}
	if err := o.requestInto("GET", "/api/state", nil, &wrong); err == nil || err.Error() != "json: cannot unmarshal bool into Go struct field .paused of type string" {
		t.Fatal(err)
	}
}

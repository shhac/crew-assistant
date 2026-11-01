package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	libcli "github.com/shhac/lib-agent-cli/cli"
	"github.com/spf13/cobra"
)

func TestTaskCheckCommands(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		status string
		fail   bool
	}{
		{[]string{"check", "CA-1", "1", "--done"}, "done", false},
		{[]string{"check", "CA-1", "Live check", "--skip", "--note", "not needed"}, "skipped", false},
		{[]string{"check", "CA-1", "1", "--undo"}, "outstanding", false},
		{[]string{"check", "CA-1", "1", "--skip"}, "", true},
		{[]string{"check", "CA-1", "1", "--done", "--undo"}, "", true},
		{[]string{"check", "CA-1", "0", "--done"}, "", true},
		{[]string{"checks"}, "", false},
		{[]string{"checks", "CA-1"}, "", false},
		{[]string{"checks", "CA-1", "--project", "other"}, "", true},
		{[]string{"checks", "CA-1", "--project", ""}, "", true},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			task := core.Task{ID: "task", ProjectID: "p", Number: 1, Ref: "CA-1", Checklist: []core.OwnerCheckItem{{Step: "Live check", Status: "outstanding"}}}
			snap := core.Snapshot{Projects: []core.Project{{ID: "p", Prefix: "CA"}}, Tasks: []core.Task{task}, AfterLanding: []core.OutstandingCheck{{TaskID: "task", Step: "Live check"}}}
			posted := false
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(snap) })
			mux.HandleFunc("POST /api/projects/p/tasks/task/owner-checks", func(w http.ResponseWriter, r *http.Request) {
				posted = true
				var in map[string]string
				json.NewDecoder(r.Body).Decode(&in)
				if in["step"] != "Live check" || in["status"] != tc.status {
					t.Error(in)
				}
				json.NewEncoder(w).Encode(task)
			})
			o := standIn(t, mux)
			o.globals = &libcli.Globals{Format: "ndjson"}
			root := &cobra.Command{Use: "crew"}
			registerTask(root, o)
			root.SetArgs(append([]string{"task"}, tc.args...))
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			output, err := os.CreateTemp(t.TempDir(), "output")
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			original := os.Stdout
			os.Stdout = output
			defer func() { os.Stdout = original }()
			err = root.Execute()
			os.Stdout = original
			if (err != nil) != tc.fail {
				t.Fatal(err)
			}
			if posted != (tc.status != "") {
				t.Fatal("unexpected request", posted)
			}
			if !tc.fail {
				if _, err := output.Seek(0, 0); err != nil {
					t.Fatal(err)
				}
				var got json.RawMessage
				if err := json.NewDecoder(output).Decode(&got); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(got), "Live check") {
					t.Fatal("missing emitted checklist", string(got))
				}
			}
		})
	}
}

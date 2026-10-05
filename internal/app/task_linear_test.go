package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/connections"
	"github.com/shhac/crew-assistant/internal/lifecycle"
)

const taskIssueID = "22222222-2222-2222-2222-222222222222"
const taskIssueRead = `{"issue":{"id":"22222222-2222-2222-2222-222222222222","identifier":"EX-1","title":"Work","url":"https://linear.app/issue/EX-1"}}`

func TestLinkTaskLinearFallbackAndAssistantTools(t *testing.T) {
	a, p := readyLinearProject(t)
	ctx := context.Background()
	task, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	a.connectionClient = connections.Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[0] == "auth" {
			return []byte(`{"alias":"home"}`), nil
		}
		reads++
		if args[len(args)-1] != "home" {
			t.Fatal(args)
		}
		switch args[2] {
		case connections.LinearIssueRefQuery:
			return []byte(taskIssueRead), nil
		case connections.LinearIssueContextQuery:
			return []byte(`{"issue":{"id":"` + taskIssueID + `","description":"Renew memberships on time."}}`), nil
		}
		t.Fatal(args)
		return nil, nil
	}}
	out, err := a.LinkTaskLinear(ctx, p.ID, task.ID, "", "", "issue", "EX-1", core.LinkedByOwner)
	if err != nil || len(out.LinearLinks) != 1 || out.LinearLinks[0].ConnectionID != "lin" || out.LinearLinks[0].By != "owner" || out.LinearLinks[0].Description != "Renew memberships on time." {
		t.Fatal(out, err)
	}
	if _, err = a.UnlinkTaskLinear(ctx, p.ID, task.ID, "issue", taskIssueID, core.LinkedByOwner); err != nil {
		t.Fatal(err)
	}
	if reads != 2 {
		t.Fatal("removal read Linear")
	}
	args := map[string]string{"project_id": p.ID, "task_id": task.Ref, "connection_id": "", "profile": "", "kind": "issue", "ref": "EX-1"}
	v, err := blockerCall(t, a, "link_task_linear", args)
	if err != nil {
		t.Fatal(err)
	}
	if v.(core.Task).LinearLinks[0].By != core.LinkedByAssistant {
		t.Fatal(v)
	}
	args = map[string]string{"project_id": p.ID, "task_id": task.Ref, "kind": "issue", "ref": taskIssueID}
	if _, err = blockerCall(t, a, "unlink_task_linear", args); err != nil {
		t.Fatal(err)
	}
	args["ref"] = "-EX-1"
	if _, err = blockerCall(t, a, "link_task_linear", args); err == nil {
		t.Fatal("bad tool ref accepted")
	}
}
func TestLinkTaskLinearFailuresLeaveStateUntouched(t *testing.T) {
	for _, mode := range []string{"read", "no-connection", "demo", "stopping", "binding-disappears", "stop-after-read", "missing-task"} {
		t.Run(mode, func(t *testing.T) {
			a, p := readyLinearProject(t)
			ctx := context.Background()
			task, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Work"})
			if err != nil {
				t.Fatal(err)
			}
			cancelCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			calls := 0
			a.connectionClient = connections.Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
				calls++
				if args[0] == "auth" {
					return []byte(`{"alias":"home"}`), nil
				}
				switch mode {
				case "read":
					return nil, errors.New("secret output")
				case "binding-disappears":
					cfg := a.Config()
					cfg.Connections = nil
					if err := a.UpdateConfig(cfg); err != nil {
						t.Fatal(err)
					}
				case "stop-after-read":
					cancel()
				}
				return []byte(taskIssueRead), nil
			}}
			switch mode {
			case "no-connection":
				if _, err := a.Core.ClearProjectLinear(ctx, p.ID); err != nil {
					t.Fatal(err)
				}
			case "demo":
				a.Demo = true
			case "stopping":
				a.setStop(lifecycle.Now(cancelCtx))
				cancel()
			case "stop-after-read":
				a.setStop(lifecycle.Now(cancelCtx))
			case "missing-task":
				task.ID = "missing"
			}
			before, _ := a.Core.Snapshot(ctx)
			_, err = a.LinkTaskLinear(ctx, p.ID, task.ID, "", "", "issue", "EX-1", core.LinkedByOwner)
			if err == nil || strings.Contains(err.Error(), "secret output") {
				t.Fatal(err)
			}
			after, _ := a.Core.Snapshot(ctx)
			if !reflect.DeepEqual(before, after) {
				b, _ := json.Marshal(after)
				t.Fatal("failed read changed state", string(b))
			}
			if mode == "read" && calls != 2 {
				t.Fatal("expected discovery then failed read", calls)
			}
		})
	}
}

func TestLinkTaskLinearInvalidKindDoesNotDiscover(t *testing.T) {
	a, p := readyLinearProject(t)
	ctx := context.Background()
	task, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	a.connectionClient = connections.Client{Run: func(context.Context, string, []string) ([]byte, error) {
		t.Fatal("invalid kind invoked lin")
		return nil, nil
	}}
	if _, err := a.LinkTaskLinear(ctx, p.ID, task.ID, "", "", "team", "EX-1", core.LinkedByOwner); err == nil {
		t.Fatal("invalid kind accepted")
	}
}

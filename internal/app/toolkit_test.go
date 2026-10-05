package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/toolkit"
)

func TestToolkitIsNeverAModelTool(t *testing.T) {
	for name := range toolActions {
		for _, word := range []string{"toolkit", "install", "brew", "skill"} {
			if strings.Contains(name, word) {
				t.Errorf("model tool %q reaches the toolkit", name)
			}
		}
	}
	a := testApp(t)
	a.Toolkit = toolkit.New(toolkit.Options{Run: func(context.Context, toolkit.Command) error {
		t.Fatal("a model call ran a command")
		return nil
	}})
	for _, name := range []string{"toolkit", "install_tool", "update_tool"} {
		if _, err := a.Execute(context.Background(), name, json.RawMessage(`{"id":"lin"}`)); err == nil {
			t.Errorf("%s ran", name)
		}
	}
}

func TestToolkitRefusesDemoAndStopping(t *testing.T) {
	a := testApp(t)
	a.Toolkit = toolkit.New(toolkit.Options{Run: func(context.Context, toolkit.Command) error {
		t.Fatal("ran a command")
		return nil
	}})
	a.Demo = true
	if _, err := a.ToolkitOverview(context.Background(), false); !errors.Is(err, ErrToolkitDemo) {
		t.Fatalf("demo overview: %v", err)
	}
	if _, err := a.StartToolkitJob("lin", toolkit.ActionInstall); !errors.Is(err, ErrToolkitDemo) {
		t.Fatalf("demo install: %v", err)
	}
	if _, err := a.UpdateAllTools(context.Background(), "brew upgrade shhac/tap/lin"); !errors.Is(err, ErrToolkitDemo) {
		t.Fatalf("demo update all: %v", err)
	}
	if _, err := a.ToolkitJob("x", 0); !errors.Is(err, ErrToolkitDemo) {
		t.Fatalf("demo job: %v", err)
	}
	a.Demo = false
	stopped, stopTaking := context.WithCancel(context.Background())
	stopTaking()
	a.setStop(lifecycle.Stop{Graceful: stopped, Force: context.Background()})
	if _, err := a.StartToolkitJob("lin", toolkit.ActionInstall); !errors.Is(err, ErrStopping) {
		t.Fatalf("install while stopping: %v", err)
	}
	if _, err := a.UpdateAllTools(context.Background(), "brew upgrade shhac/tap/lin"); !errors.Is(err, ErrStopping) {
		t.Fatalf("update all while stopping: %v", err)
	}
}

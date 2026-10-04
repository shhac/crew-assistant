package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/lifecycle"
)

func TestConfigurationApplicationDoesNotWaitOnSchedulingAdmission(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := config.Default()
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := core.NewService(store, cfg)
	a := New(service, cfg, path, Options{})
	p, err := service.CreateProject(t.Context(), core.ProjectInput{Title: "Synthetic", Brief: core.BriefInput{Goal: "Test", Criteria: []string{"Test"}}, Template: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.QueueTask(t.Context(), p.ID, core.TaskInput{Objective: "Synthetic", Criteria: []string{"Test"}}); err != nil {
		t.Fatal(err)
	}
	cfg.Slack.ProjectID = p.ID
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	entered, readConfig, abandon := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	scheduled := make(chan error, 1)
	go func() {
		_, err := service.Schedule(t.Context(), func(core.Role) string {
			once.Do(func() { close(entered) })
			select {
			case <-readConfig:
				_ = a.Config()
			case <-abandon:
			}
			return ""
		})
		scheduled <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(abandon)
		t.Fatal("scheduling never reached admission")
	}
	applied := make(chan error, 1)
	go func() {
		_, err := a.SetAutopilotMode("authorised-research", autopilot.Act, 0)
		applied <- err
	}()
	select {
	case err := <-applied:
		close(readConfig)
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		close(abandon)
		<-scheduled
		<-applied
		t.Fatal("configuration waited for Store.mu held by scheduling admission")
	}
	if err := <-scheduled; err != nil {
		t.Fatal(err)
	}
}

func TestAutopilotModeEditRetainsRuntimeOverridesAndRejectedEditsKeepAdmission(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	disk := config.Default()
	if err := config.Save(path, disk); err != nil {
		t.Fatal(err)
	}
	cfg := disk
	cfg.Dashboard.Addr = "127.0.0.1:8341"
	cfg.Dashboard.Tailscale = "serve"
	cfg.Dashboard.TailscalePort = 10000
	cfg.Dashboard.AllowedUsers = []string{"synthetic@example.test"}
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := core.NewService(store, cfg)
	a := New(service, cfg, path, Options{})
	p, err := service.CreateProject(context.Background(), core.ProjectInput{Title: "Synthetic", Brief: core.BriefInput{Goal: "Test", Criteria: []string{"Test"}}, Template: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := a.Autopilot.Register("authorised-research", "v1", []string{"rename-project"}, func(core.Snapshot, core.ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetAutopilotMode("authorised-research", autopilot.Act, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReloadConfig(); err != nil {
		t.Fatalf("watcher reconciliation rejected runtime overrides: %v", err)
	}
	if a.Config().Dashboard.Addr != cfg.Dashboard.Addr || a.Config().Dashboard.Tailscale != cfg.Dashboard.Tailscale || a.Config().Dashboard.TailscalePort != cfg.Dashboard.TailscalePort {
		t.Fatal("runtime overrides replaced")
	}
	invalid := a.Config()
	invalid.Dashboard.Tailscale = "invalid"
	if err := a.UpdateConfig(invalid); err == nil {
		t.Fatal("invalid whole config accepted")
	}
	for _, edit := range []struct {
		mode     autopilot.Mode
		revision uint64
	}{{"invalid", 1}, {autopilot.Off, 0}} {
		if _, err := a.SetAutopilotMode("authorised-research", edit.mode, edit.revision); err == nil {
			t.Fatal("rejected edit succeeded")
		}
		v, err := service.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		out, err := f.Submit(context.Background(), fmt.Sprintf("after-%s", edit.mode), "reason", core.ConcreteAction{Kind: "rename-project", ProjectID: p.ID, TargetVersion: v.Projects[0].TitleRevision, Args: json.RawMessage(`{"title":"Allowed"}`)})
		if err != nil || out.Status != "performed" {
			t.Fatalf("rejected edit blocked admission: %+v %v", out, err)
		}
	}
	loaded, err := config.Load(path)
	if err != nil || loaded.Autopilot.Revisions["authorised-research"] != 1 || loaded.Dashboard.Addr != disk.Dashboard.Addr {
		t.Fatalf("disk config changed unexpectedly: %+v %v", loaded, err)
	}
	if _, err := a.SetAutopilotMode("authorised-research", "", 1); err != nil {
		t.Fatal(err)
	}
	mode, err := a.Config().Autopilot.EffectiveMode("authorised-research")
	if err != nil || mode != autopilot.Suggest {
		t.Fatalf("unset: %s %v", mode, err)
	}
	loaded, err = config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Dashboard.Addr = "127.0.0.1:9999"
	if err := config.Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReloadConfig(); err == nil {
		t.Fatal("actual connection edit must still require restart")
	}
}

func TestAutopilotRuntimeAdmissionHonorsNoDispatchAndStopping(t *testing.T) {
	for _, state := range []string{"no-dispatch", "stopping", "upgrade"} {
		t.Run(state, func(t *testing.T) {
			cfg := config.Default()
			store, err := core.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			service := core.NewService(store, cfg)
			a := New(service, cfg, filepath.Join(t.TempDir(), "config.json"), Options{})
			p, err := service.CreateProject(context.Background(), core.ProjectInput{Title: "Synthetic", Brief: core.BriefInput{Goal: "Test", Criteria: []string{"Test"}}, Template: "draft"})
			if err != nil {
				t.Fatal(err)
			}
			f, err := a.Autopilot.Register("authorised-research", "v1", []string{"rename-project"}, func(core.Snapshot, core.ConcreteAction) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			switch state {
			case "no-dispatch":
				a.dispatchDisabled.Store(true)
			case "upgrade":
				a.upgrading.Store(true)
			case "stopping":
				ctx, cancel := context.WithCancel(context.Background())
				a.setStop(lifecycle.Now(ctx))
				cancel()
			}
			out, err := f.Submit(context.Background(), "source", "reason", core.ConcreteAction{Kind: "rename-project", ProjectID: p.ID, Args: json.RawMessage(`{"title":"Changed"}`)})
			if err != nil || out.Status != "refused" {
				t.Fatalf("admission: %+v %v", out, err)
			}
		})
	}
}

func TestAutopilotPersistenceThenApplicationFailureRequiresReconciliation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := config.Default()
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := core.NewService(store, cfg)
	a := New(s, cfg, path, Options{})
	p, err := s.CreateProject(t.Context(), core.ProjectInput{Title: "Synthetic", Brief: core.BriefInput{Goal: "Test", Criteria: []string{"Test"}}, Template: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := a.Autopilot.Register("authorised-research", "v1", []string{"rename-project"}, func(core.Snapshot, core.ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	a.applyConfiguration = func(config.Config) error { return errors.New("injected application failure") }
	if _, err := a.SetAutopilotMode("authorised-research", autopilot.Act, 0); err == nil {
		t.Fatal("application failure reported success")
	}
	disk, err := config.Load(path)
	if err != nil || disk.Autopilot.Modes["authorised-research"] != autopilot.Act {
		t.Fatal("failure was not after persistence", disk, err)
	}
	proposal := core.ConcreteAction{Kind: "rename-project", ProjectID: p.ID, TargetVersion: p.TitleRevision, Args: json.RawMessage(`{"title":"Changed"}`)}
	out, err := f.Submit(t.Context(), "divergent", "reason", proposal)
	if err != nil || out.Status != "refused" {
		t.Fatal("divergent configuration admitted work", out, err)
	}
	a.applyConfiguration = nil
	if _, err := a.ReloadConfig(); err != nil {
		t.Fatal(err)
	}
	out, err = f.Submit(t.Context(), "reconciled", "reason", proposal)
	if err != nil || out.Status != "performed" {
		t.Fatal("reconciliation failed to reopen admission", out, err)
	}
}

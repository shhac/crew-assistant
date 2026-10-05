package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/upgrade"
)

func awaitCallback[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("callback test timed out")
		var zero T
		return zero
	}
}

func TestShutdownWaitsForHTTPApprovedCallback(t *testing.T) {
	for _, reason := range []string{"shutdown", "force", "upgrade"} {
		t.Run(reason, func(t *testing.T) {
			dir := t.TempDir()
			store, err := core.Open(filepath.Join(dir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			cfg := config.Default()
			cfg.Autopilot.Modes = map[string]autopilot.Mode{autopilot.Operator: autopilot.Suggest}
			cfg.Upgrade.Mode = "automatic"
			signals := make(chan os.Signal, 2)
			stop, cancel := lifecycle.Watch(context.Background(), signals, func(string) {})
			defer cancel()
			var requests atomic.Int32
			service := core.NewService(store, cfg)
			a := app.New(service, cfg, filepath.Join(dir, "config.json"), app.Options{Version: "v1.0.0", RequestUpgrade: func(string, bool) error {
				requests.Add(1)
				if !stop.Drain("upgrade") {
					return errors.New("drain refused")
				}
				return nil
			}})
			// Suppress real agent admission while exercising App.Run's supervision.
			a.Work.Demo = true
			auth, err := NewAuth(dir, "http://127.0.0.1:8340", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			handler := New(a, auth)
			p, err := service.CreateProject(t.Context(), core.ProjectInput{Title: "Synthetic", Template: "draft"})
			if err != nil {
				t.Fatal(err)
			}
			p, err = service.SetOperatorPermission(t.Context(), p.ID, true, 0)
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan context.Context, 1)
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			err = a.Autopilot.RegisterExternal("synthetic", core.ExternalAction{
				Check: func(core.Snapshot, core.ConcreteAction) error { return nil },
				Execute: func(ctx context.Context, _ core.AutopilotAction) (core.ExternalResult, error) {
					entered <- ctx
					select {
					case <-release:
						return core.ExternalResult{Outcome: "performed", Evidence: "HTTP effect finished"}, nil
					case <-ctx.Done():
						return core.ExternalResult{}, ctx.Err()
					}
				},
				Reconcile: func(context.Context, core.AutopilotAction) (core.ExternalResult, error) {
					return core.ExternalResult{Outcome: "performed", Evidence: "found"}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			f, err := a.Autopilot.Register(autopilot.Operator, "v1", []string{"synthetic"}, func(core.Snapshot, core.ConcreteAction) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			ready := make(chan struct{})
			var readyOnce sync.Once
			if err = f.OnEvents([]string{"heartbeat"}, func(context.Context, core.Snapshot, core.AutopilotEvent) ([]core.EventProposal, error) {
				readyOnce.Do(func() { close(ready) })
				return nil, nil
			}); err != nil {
				t.Fatal(err)
			}
			action := core.ConcreteAction{Kind: "synthetic", ProjectID: p.ID, PermissionRevision: p.OperatorPermission.Revision, Args: json.RawMessage("{}")}
			first, err := f.Submit(t.Context(), "first", "reason", action)
			if err != nil {
				t.Fatal(err)
			}
			second, err := f.Submit(t.Context(), "second", "reason", action)
			if err != nil {
				t.Fatal(err)
			}
			run := make(chan error, 1)
			go func() { run <- a.Run(stop, false) }()
			awaitCallback(t, ready)
			response := make(chan *httptest.ResponseRecorder, 1)
			requestCtx, endRequest := context.WithCancel(context.Background())
			go func() {
				req := httptest.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:8340/api/autopilot/actions/%s/approve", first.ID), strings.NewReader(`{"revision":1}`)).WithContext(requestCtx)
				// Exercise the existing owner authentication and mutation middleware.
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+auth.admin)
				req.RemoteAddr = "127.0.0.1:4321"
				req.Header.Set("X-Requested-With", "crew-assistant")
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, req)
				response <- recorder
			}()
			callbackCtx := awaitCallback(t, entered)
			endRequest() // A disconnected HTTP client does not revoke admitted work.
			if reason == "upgrade" {
				if err := service.RecordUpdateCheck(t.Context(), "v1.0.0", upgrade.Result{Available: "v2.0.0"}); err != nil {
					t.Fatal(err)
				}
				waiting, err := a.UpgradeWaiting(t.Context())
				if err != nil || len(waiting) != 1 || waiting[0].Kind != "autopilot" || waiting[0].Ref != first.ID {
					t.Fatal(waiting, err)
				}
				if err := a.RequestUpgrade("v2.0.0", true); err != nil || requests.Load() != 0 {
					t.Fatal("upgrade admitted live callback", err)
				}
				if err := a.RequestUpgrade("v2.0.0", false); err != nil || requests.Load() != 1 {
					t.Fatal(err)
				}
			} else {
				if !stop.Drain("signal") {
					t.Fatal("drain refused")
				}
			}
			select {
			case err := <-run:
				t.Fatal("Run returned before receipt", err)
			case <-time.After(40 * time.Millisecond):
			}
			if callbackCtx.Err() != nil {
				t.Fatal("graceful stop cancelled callback")
			}
			later := send(handler, auth, "POST", fmt.Sprintf("/api/autopilot/actions/%s/approve", second.ID), strings.NewReader(`{"revision":1}`), asOwner)
			if later.Code == 200 {
				t.Fatal("new HTTP action admitted during drain")
			}
			pending, _ := a.Autopilot.Action(t.Context(), second.ID)
			if pending.Status != "proposed" {
				t.Fatal("pending action consumed", pending)
			}
			if reason == "force" {
				cancel()
			} else {
				once.Do(func() { close(release) })
			}
			reply := awaitCallback(t, response)
			if reply.Code != 200 {
				t.Fatal(reply.Code, reply.Body.String())
			}
			if err := awaitCallback(t, run); err != nil {
				t.Fatal(err)
			}
			got, err := a.Autopilot.Action(context.Background(), first.ID)
			want := "performed"
			if reason == "force" {
				want = "uncertain"
			}
			if err != nil || got.Status != want || len(service.ExternalCallbacks()) != 0 {
				t.Fatal("Run finished before saved receipt", got, err)
			}
		})
	}
}

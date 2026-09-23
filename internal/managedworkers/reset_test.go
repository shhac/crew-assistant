package managedworkers

import (
	"context"
	"errors"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/workerbroker"
)

type idleBroker struct {
	fakeBroker
	busy     bool
	quiesced bool
}

func (b *idleBroker) QuiesceIdle() error {
	if b.busy {
		return errors.New("worker cleanup still active")
	}
	b.quiesced = true
	return nil
}

func TestIdleResetRetainsStateAndChangesOnlyModel(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.runtime = &fakeProvisioner{}
	var brokers []*idleBroker
	m.open = func(cfg workerbroker.Config) (broker, error) {
		b := &idleBroker{fakeBroker: fakeBroker{cfg: cfg}}
		brokers = append(brokers, b)
		return b, nil
	}
	workspace := t.TempDir()
	model := config.Default().WorkerModel
	if _, err := m.Prepare(context.Background(), "project", workspace, model); err != nil {
		t.Fatal(err)
	}
	brokers[0].busy = true
	if err := m.ResetIdle("project"); err == nil {
		t.Fatal("reset active runtime")
	}
	if brokers[0].stopped.Load() || brokers[0].closed || brokers[0].quiesced {
		t.Fatal("cancelled active work")
	}
	brokers[0].busy = false
	if err := m.ResetIdle("project"); err != nil {
		t.Fatal(err)
	}
	if !brokers[0].stopped.Load() || !brokers[0].closed || !brokers[0].quiesced {
		t.Fatal("did not release idle runtime")
	}
	model.Effort = "low"
	if _, err := m.Client(context.Background(), "project", workspace, model); err != nil {
		t.Fatal(err)
	}
	if len(brokers) != 2 || brokers[1].cfg.StateDir != brokers[0].cfg.StateDir || brokers[1].cfg.Effort != "low" || brokers[1].cfg.AuthToken == brokers[0].cfg.AuthToken {
		t.Fatal("reconfiguration lost state or retained old settings")
	}
}

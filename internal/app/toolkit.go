package app

import (
	"context"
	"errors"

	"github.com/shhac/crew-assistant/internal/toolkit"
)

// ErrToolkitDemo keeps demo mode from inspecting or changing this machine.
var ErrToolkitDemo = errors.New("tool setup is unavailable in demo mode")

// These are the owner's dashboard actions only: no model or role tool
// reaches the toolkit, so nothing an agent writes can install software.
func (a *App) ToolkitOverview(ctx context.Context, refresh bool) (toolkit.Overview, error) {
	if a.Demo {
		return toolkit.Overview{}, ErrToolkitDemo
	}
	return a.Toolkit.List(ctx, refresh), nil
}

func (a *App) StartToolkitJob(tool, action string) (toolkit.JobView, error) {
	if err := a.admitToolkitJob(); err != nil {
		return toolkit.JobView{}, err
	}
	return a.Toolkit.Start(tool, action)
}

// UpdateAllTools runs only the command the owner confirmed; it is compared,
// never run as given.
func (a *App) UpdateAllTools(ctx context.Context, confirmed string) (toolkit.JobView, error) {
	if err := a.admitToolkitJob(); err != nil {
		return toolkit.JobView{}, err
	}
	return a.Toolkit.UpdateAll(ctx, confirmed)
}

func (a *App) admitToolkitJob() error {
	if a.Demo {
		return ErrToolkitDemo
	}
	return a.refuseWhileStopping()
}

func (a *App) ToolkitJob(id string, after int) (toolkit.JobView, error) {
	if a.Demo {
		return toolkit.JobView{}, ErrToolkitDemo
	}
	return a.Toolkit.Job(id, after)
}

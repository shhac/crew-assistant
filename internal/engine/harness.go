package engine

import (
	"context"

	"github.com/shhac/lib-agent-harness/completion"
)

type ModelOption = completion.ModelOption
type ModelEffort = completion.ModelEffort

func harnessConfig(cfg Config) completion.Config {
	return completion.Config{
		Engine: cfg.Engine, Model: cfg.Model, Effort: cfg.Effort,
		CodexBin: cfg.CodexBin, CodexHome: cfg.CodexHome,
		ClaudeBin: cfg.ClaudeBin, ClaudeHome: cfg.ClaudeHome,
		WorkDirRoot: cfg.WorkDirRoot, MaxContextBytes: cfg.MaxContextBytes,
		Timeout: cfg.Timeout, BeforeRequest: cfg.BeforeRequest,
	}
}
func DiscoverModels(ctx context.Context, cfg Config) ([]ModelOption, error) {
	return completion.DiscoverModels(ctx, harnessConfig(cfg))
}
func ValidateCodexHome(home string) error             { return completion.ValidateCodexHome(home) }
func CodexEnvironment(home string) ([]string, error)  { return completion.CodexEnvironment(home) }
func ClaudeEnvironment(home string) ([]string, error) { return completion.ClaudeEnvironment(home) }

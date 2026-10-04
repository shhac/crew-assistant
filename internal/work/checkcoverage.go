package work

import (
	"encoding/json"

	"github.com/shhac/crew-assistant/internal/checktest"
	"github.com/shhac/lib-agent-harness/sandbox"
)

type checkCoveragePage struct {
	Stage                string           `json:"stage"`
	Complete             bool             `json:"complete"`
	GoComplete           bool             `json:"go_complete"`
	SkipCount            int              `json:"skip_count"`
	RequiredSkipFailures int              `json:"required_skip_failures"`
	Skips                []checktest.Skip `json:"skips"`
	More                 bool             `json:"more"`
	Next                 string           `json:"next,omitempty"`
}

// Subsequent run_check calls drain evidence pages before starting another run.
// No report path survives cleanup, and no counter or evidence crosses invocations.
func checkResultJSON(result sandbox.CommandResult, p *checktest.Progress, offset int) (string, int, error) {
	if p == nil {
		data, err := commandResultJSON(result)
		return data, 0, err
	}
	total := len(p.Report.Skips)
	end := min(offset+32, total)
	if offset > 0 {
		result.Stdout, result.Stderr = "", ""
	}
	result = boundedCommandResult(result)
	for {
		page := checkCoveragePage{Stage: p.Stage, Complete: p.Done && p.Stage == "complete" && result.ExitCode == 0 && !result.TimedOut && !p.Report.Failed,
			GoComplete: p.Report.Complete, SkipCount: total, Skips: p.Report.Skips[offset:end], More: end < total}
		for _, skip := range p.Report.Skips {
			if skip.Exception == "" {
				page.RequiredSkipFailures++
			}
		}
		if page.More {
			page.Next = "Call run_check again for the remaining skip evidence; this continues the same check."
		}
		data, err := json.Marshal(struct {
			sandbox.CommandResult
			Coverage checkCoveragePage `json:"coverage"`
		}{result, page})
		if err != nil || len(data) <= 120<<10 {
			return string(data), end, err
		}
		if end-offset > 1 {
			end = offset + (end-offset)/2
			continue
		}
		result.Stdout, _ = commandTail(result.Stdout, len(result.Stdout)/2)
		result.Stderr, _ = commandTail(result.Stderr, len(result.Stderr)/2)
		result.Truncated = true
	}
}

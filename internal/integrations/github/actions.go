package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Job is one GitHub Actions job of a workflow run.
type Job struct {
	ID         int64     `json:"id"`
	RunID      int64     `json:"run_id"`
	Name       string    `json:"name"`
	Conclusion string    `json:"conclusion"`
	StartedAt  time.Time `json:"started_at"`
	URL        string    `json:"html_url"`
}

// Failed reports a job that finished badly.
func (j Job) Failed() bool { return failing[strings.ToUpper(j.Conclusion)] }

var actionsJobURL = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9-]+/[A-Za-z0-9._-]+)/actions/runs/(\d+)/job/(\d+)`)

// ActionsJob is the GitHub Actions run and job behind a check on repo, read
// from where the check links to; a status from anywhere else has none.
func (c Check) ActionsJob(repo string) (run, job int64, ok bool) {
	m := actionsJobURL.FindStringSubmatch(c.Link())
	if m == nil || !strings.EqualFold(m[1], repo) {
		return 0, 0, false
	}
	run, runErr := strconv.ParseInt(m[2], 10, 64)
	job, jobErr := strconv.ParseInt(m[3], 10, 64)
	return run, job, runErr == nil && jobErr == nil
}

// RunJobs lists the latest attempt's jobs of a workflow run.
func (c Client) RunJobs(ctx context.Context, repo string, run int64) ([]Job, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	out, err := c.Run(ctx, "api", "--paginate", fmt.Sprintf("repos/%s/actions/runs/%d/jobs?filter=latest&per_page=100", repo, run))
	if err != nil {
		return nil, err
	}
	// Paginated, gh prints one page after another.
	var jobs []Job
	pages := json.NewDecoder(bytes.NewReader(out))
	for {
		var page struct {
			Jobs []Job `json:"jobs"`
		}
		err := pages.Decode(&page)
		if errors.Is(err, io.EOF) {
			return jobs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("gh gave unreadable workflow jobs: %w", err)
		}
		jobs = append(jobs, page.Jobs...)
	}
}

// JobLog is everything a GitHub Actions job printed.
func (c Client) JobLog(ctx context.Context, repo string, job int64) (string, error) {
	if err := checkRepo(repo); err != nil {
		return "", err
	}
	out, err := c.Run(ctx, "api", fmt.Sprintf("repos/%s/actions/jobs/%d/logs", repo, job))
	return string(out), err
}

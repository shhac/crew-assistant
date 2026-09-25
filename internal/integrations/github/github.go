// Package github reads and acts on pull requests through the owner's own gh
// login. It never holds a token: gh does, and git borrows it through gh's
// credential helper.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/procgroup"
)

// Runner runs gh with args and returns what it printed.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// Client talks to GitHub. Run is replaced in tests.
type Client struct{ Run Runner }

func New() Client { return Client{Run: runGH} }

func runGH(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	procgroup.Detach(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if len(detail) > 300 {
			detail = detail[:300]
		}
		return nil, fmt.Errorf("gh %s: %s", args[0], detail)
	}
	return stdout.Bytes(), nil
}

// URL is where git pushes and fetches for a repository.
func URL(repo string) string { return "https://github.com/" + repo + ".git" }

// CredentialConfig lets git borrow gh's login for github.com and nothing else.
func CredentialConfig() []string {
	return []string{"-c", "credential.helper=", "-c", "credential.https://github.com.helper=!gh auth git-credential"}
}

type Author struct {
	Login string `json:"login"`
}

type Review struct {
	Author      Author    `json:"author"`
	State       string    `json:"state"`
	Body        string    `json:"body"`
	SubmittedAt time.Time `json:"submittedAt"`
}

type Comment struct {
	Author    Author    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

// Check is one entry of the status rollup: a check run or a commit status.
type Check struct {
	Name       string `json:"name"`
	Context    string `json:"context"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
	DetailsURL string `json:"detailsUrl"`
	TargetURL  string `json:"targetUrl"`
}

func (c Check) Label() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Context
}

func (c Check) Link() string {
	if c.DetailsURL != "" {
		return c.DetailsURL
	}
	return c.TargetURL
}

var failing = map[string]bool{"FAILURE": true, "ERROR": true, "CANCELLED": true, "TIMED_OUT": true, "ACTION_REQUIRED": true, "STARTUP_FAILURE": true}

// Failed reports a check that finished badly.
func (c Check) Failed() bool { return failing[c.Conclusion] || failing[c.State] }

// Pending reports a check that has not finished.
func (c Check) Pending() bool {
	if c.State != "" {
		return c.State == "PENDING" || c.State == "EXPECTED"
	}
	return c.Status != "COMPLETED"
}

type PR struct {
	Number           int       `json:"number"`
	URL              string    `json:"url"`
	State            string    `json:"state"`
	Mergeable        string    `json:"mergeable"`
	MergeStateStatus string    `json:"mergeStateStatus"`
	ReviewDecision   string    `json:"reviewDecision"`
	HeadRefOid       string    `json:"headRefOid"`
	Checks           []Check   `json:"statusCheckRollup"`
	Reviews          []Review  `json:"reviews"`
	Comments         []Comment `json:"comments"`
	MergeCommit      *struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
}

// CheckState is the combined state of the checks: FAILURE, PENDING, SUCCESS,
// or NONE when there are none.
func (p PR) CheckState() string {
	if len(p.Checks) == 0 {
		return "NONE"
	}
	state := "SUCCESS"
	for _, c := range p.Checks {
		if c.Failed() {
			return "FAILURE"
		}
		if c.Pending() {
			state = "PENDING"
		}
	}
	return state
}

// Feedback is a review or comment written after since, newest last.
type Feedback struct {
	Author, Kind, Body string
	At                 time.Time
}

// FeedbackSince lists reviews and comments newer than since.
func (p PR) FeedbackSince(since time.Time) []Feedback {
	var out []Feedback
	for _, r := range p.Reviews {
		if r.SubmittedAt.After(since) {
			out = append(out, Feedback{Author: r.Author.Login, Kind: "review (" + strings.ToLower(strings.ReplaceAll(r.State, "_", " ")) + ")", Body: r.Body, At: r.SubmittedAt})
		}
	}
	for _, c := range p.Comments {
		if c.CreatedAt.After(since) {
			out = append(out, Feedback{Author: c.Author.Login, Kind: "comment", Body: c.Body, At: c.CreatedAt})
		}
	}
	slices.SortStableFunc(out, func(a, b Feedback) int { return a.At.Compare(b.At) })
	return out
}

// Latest is the time of the newest review or comment.
func (p PR) Latest() time.Time {
	var latest time.Time
	for _, r := range p.Reviews {
		latest = later(latest, r.SubmittedAt)
	}
	for _, c := range p.Comments {
		latest = later(latest, c.CreatedAt)
	}
	return latest
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// Ready reports a pull request the platform would merge now: approved (or
// needing no review), every check green, and nothing behind or in conflict.
func (p PR) Ready() bool {
	if p.State != "OPEN" || p.Mergeable != "MERGEABLE" {
		return false
	}
	if p.ReviewDecision != "" && p.ReviewDecision != "APPROVED" {
		return false
	}
	if s := p.CheckState(); s != "SUCCESS" && s != "NONE" {
		return false
	}
	return p.MergeStateStatus == "CLEAN" || p.MergeStateStatus == "HAS_HOOKS"
}

// Behind reports a base that moved on, or a conflict with it.
func (p PR) Behind() bool {
	return p.MergeStateStatus == "BEHIND" || p.MergeStateStatus == "DIRTY" || p.Mergeable == "CONFLICTING"
}

// PRRef names a pull request as owner/name#number, the form wakes store.
type PRRef struct {
	Repo   string
	Number int
}

func (r PRRef) String() string { return r.Repo + "#" + strconv.Itoa(r.Number) }

// ParsePRRef reads owner/name#number.
func ParsePRRef(s string) (PRRef, error) {
	repo, number, ok := strings.Cut(s, "#")
	n, err := strconv.Atoi(number)
	if !ok || err != nil || n < 1 || !repoName.MatchString(repo) {
		return PRRef{}, errors.New("a pull request is named as owner/name#number")
	}
	return PRRef{Repo: repo, Number: n}, nil
}

var repoName = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

func (c Client) View(ctx context.Context, repo string, number int) (PR, error) {
	if !repoName.MatchString(repo) {
		return PR{}, errors.New("not a GitHub repository name")
	}
	out, err := c.Run(ctx, "pr", "view", strconv.Itoa(number), "--repo", repo, "--json", "number,url,state,mergeable,mergeStateStatus,reviewDecision,headRefOid,statusCheckRollup,reviews,comments,mergeCommit")
	if err != nil {
		return PR{}, err
	}
	var pr PR
	if err = json.Unmarshal(out, &pr); err != nil {
		return PR{}, fmt.Errorf("gh gave an unreadable pull request: %w", err)
	}
	return pr, nil
}

var prNumber = regexp.MustCompile(`/pull/(\d+)\s*$`)

// Open creates a pull request and returns its number and address.
func (c Client) Open(ctx context.Context, repo, base, head, title, body string) (int, string, error) {
	out, err := c.Run(ctx, "pr", "create", "--repo", repo, "--base", base, "--head", head, "--title", title, "--body", body)
	if err != nil {
		return 0, "", err
	}
	url := strings.TrimSpace(string(out))
	match := prNumber.FindStringSubmatch(url)
	if match == nil {
		return 0, "", fmt.Errorf("gh did not report the new pull request: %q", url)
	}
	n, _ := strconv.Atoi(match[1])
	return n, url, nil
}

// FindOpen returns the open pull request from head, if there is one, so an
// opening that happened but was never recorded is picked up, not repeated.
func (c Client) FindOpen(ctx context.Context, repo, head string) (int, string, bool, error) {
	if !repoName.MatchString(repo) {
		return 0, "", false, errors.New("not a GitHub repository name")
	}
	out, err := c.Run(ctx, "pr", "list", "--repo", repo, "--head", head, "--state", "open", "--json", "number,url", "--limit", "1")
	if err != nil {
		return 0, "", false, err
	}
	var found []struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
	}
	if err = json.Unmarshal(out, &found); err != nil {
		return 0, "", false, fmt.Errorf("gh gave an unreadable pull request list: %w", err)
	}
	if len(found) == 0 {
		return 0, "", false, nil
	}
	return found[0].Number, found[0].URL, true, nil
}

// Merge merges the pull request only if its head is still head, so nothing
// pushed after the checks is merged unseen.
func (c Client) Merge(ctx context.Context, repo string, number int, method, head string) error {
	if method != "squash" && method != "merge" && method != "rebase" {
		return fmt.Errorf("unknown merge method %q", method)
	}
	_, err := c.Run(ctx, "pr", "merge", strconv.Itoa(number), "--repo", repo, "--"+method, "--match-head-commit", head)
	return err
}

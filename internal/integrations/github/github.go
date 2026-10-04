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
	"github.com/shhac/crew-assistant/internal/text"
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
		detail := text.Clip(stderr.String(), 300)
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
	Author Author `json:"author"`
	// Association is the author's standing in the repository, such as
	// OWNER, MEMBER, COLLABORATOR or NONE.
	Association string    `json:"authorAssociation"`
	State       string    `json:"state"`
	Body        string    `json:"body"`
	SubmittedAt time.Time `json:"submittedAt"`
	// Commit is the head the review was made on, which may be older than
	// the pull request's head now.
	Commit struct {
		Oid string `json:"oid"`
	} `json:"commit"`
}

type Comment struct {
	Author      Author    `json:"author"`
	Association string    `json:"authorAssociation"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"createdAt"`
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
	// MergeInFlight includes automatic merge and merge queue enrollment.
	MergeInFlight    bool      `json:"-"`
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
	// Threads are the review threads on the code, which gh's pull request
	// view leaves out; View reads them apart.
	Threads []Thread `json:"-"`
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

// Feedback is a review, comment or review-thread comment written after
// since, newest last. Commit is the head a review was made on; a comment is
// on the conversation, not a commit, and has none. Thread, Path and Line
// place a thread comment on the code.
type Feedback struct {
	Author, Association, Kind, Body, Commit string
	Thread, Path                            string
	Line                                    int
	At                                      time.Time
}

// trusted are the standings whose words the team may act on: people the
// repository's owner let in, never anyone who can merely comment.
var trusted = map[string]bool{"OWNER": true, "MEMBER": true, "COLLABORATOR": true}

// Trusted reports feedback from the repository's owner, members or
// collaborators.
func (f Feedback) Trusted() bool { return trusted[f.Association] }

// FeedbackSince lists reviews, comments and thread comments newer than since.
func (p PR) FeedbackSince(since time.Time) []Feedback {
	var out []Feedback
	for _, r := range p.Reviews {
		if r.SubmittedAt.After(since) {
			out = append(out, Feedback{Author: r.Author.Login, Association: r.Association, Kind: "review (" + strings.ToLower(strings.ReplaceAll(r.State, "_", " ")) + ")", Body: r.Body, Commit: r.Commit.Oid, At: r.SubmittedAt})
		}
	}
	for _, c := range p.Comments {
		if c.CreatedAt.After(since) {
			out = append(out, Feedback{Author: c.Author.Login, Association: c.Association, Kind: "comment", Body: c.Body, At: c.CreatedAt})
		}
	}
	for _, th := range p.Threads {
		for _, c := range th.Comments {
			if c.CreatedAt.After(since) {
				out = append(out, Feedback{Author: c.Author.Login, Association: c.Association, Kind: "thread comment", Body: c.Body, Thread: th.ID, Path: th.Path, Line: th.Line, At: c.CreatedAt})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b Feedback) int { return a.At.Compare(b.At) })
	return out
}

// Latest is the time of the newest review, comment or thread comment.
func (p PR) Latest() time.Time {
	var latest time.Time
	for _, r := range p.Reviews {
		latest = later(latest, r.SubmittedAt)
	}
	for _, c := range p.Comments {
		latest = later(latest, c.CreatedAt)
	}
	for _, th := range p.Threads {
		for _, c := range th.Comments {
			latest = later(latest, c.CreatedAt)
		}
	}
	return latest
}

// Unresolved counts the review threads still open.
func (p PR) Unresolved() int {
	n := 0
	for _, th := range p.Threads {
		if !th.Resolved {
			n++
		}
	}
	return n
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// Ready reports a pull request ready to land: approved where the
// repository asks for review, every check green, no review thread left
// unresolved, and nothing behind or in conflict.
func (p PR) Ready() bool {
	if p.State != "OPEN" || p.Mergeable != "MERGEABLE" || p.Unresolved() > 0 {
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

// checkRepo refuses anything but an owner/name repository, before it can
// reach gh's command line.
func checkRepo(repo string) error {
	if !repoName.MatchString(repo) {
		return errors.New("not a GitHub repository name")
	}
	return nil
}

// pr runs gh pr verb on a pull request of repo, with args after.
func (c Client) pr(ctx context.Context, verb, repo string, number int, args ...string) ([]byte, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	return c.Run(ctx, append([]string{"pr", verb, strconv.Itoa(number), "--repo", repo}, args...)...)
}

func (c Client) View(ctx context.Context, repo string, number int) (PR, error) {
	out, err := c.pr(ctx, "view", repo, number, "--json", "number,url,state,mergeable,mergeStateStatus,reviewDecision,headRefOid,statusCheckRollup,reviews,comments,mergeCommit")
	if err != nil {
		return PR{}, err
	}
	var pr PR
	if err = json.Unmarshal(out, &pr); err != nil {
		return PR{}, fmt.Errorf("gh gave an unreadable pull request: %w", err)
	}
	if pr.Threads, err = c.threads(ctx, repo, number, &pr); err != nil {
		return PR{}, err
	}
	return pr, nil
}

var prNumber = regexp.MustCompile(`/pull/(\d+)\s*$`)

// Open creates a pull request and returns its number and address.
func (c Client) Open(ctx context.Context, repo, base, head, title, body string) (int, string, error) {
	if err := checkRepo(repo); err != nil {
		return 0, "", err
	}
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
	if err := checkRepo(repo); err != nil {
		return 0, "", false, err
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
	_, err := c.pr(ctx, "merge", repo, number, "--"+method, "--match-head-commit", head)
	return err
}

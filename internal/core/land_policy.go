package core

import (
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// LandPolicy is what "landing" means for a code project: prose for people and
// agents, plus the few fields the loop needs to do it.
type LandPolicy struct {
	Means string `json:"means,omitempty"`
	// Via is how a change lands without pull requests: LandBranch (a new
	// local branch) or LandPush (a fast-forward push onto Target).
	Via    string `json:"via,omitempty"`
	Target string `json:"target,omitempty"`
	// Method is how a push lands: fast-forward.
	Method string `json:"method,omitempty"`
	// PullRequests lands each change through a GitHub pull request into
	// Target instead, whatever Via says.
	PullRequests bool `json:"pull_requests,omitempty"`
	// GitHub is the owner/name repository pull requests are opened on.
	GitHub string `json:"github,omitempty"`
	// Merge is how a pull request merges: squash, merge or rebase.
	Merge string `json:"merge,omitempty"`
	// Open is who decides that a change's pull request opens: OpenPM (the
	// default), OpenOwner or OpenImplementer.
	Open string `json:"open,omitempty"`
	// Approve is who approves a change landing: ApproveBefore (the owner),
	// ApproveNone, or ApprovePM (the team's PM). Without pull requests it is
	// the owner unless set, and the PM only for a push; with them it is who
	// approves a ready pull request merging, the PM unless set.
	Approve string `json:"approve,omitempty"`
	// Draft opens each pull request as a draft. Only the owner's choice
	// marks one ready for review; until then it can't be ready to merge.
	Draft bool `json:"draft,omitempty"`
	// TrustedBots are the GitHub logins of automated reviewers whose
	// comments the team weighs as advice, though the repository never let
	// them in.
	TrustedBots []string `json:"trusted_bots,omitempty"`
	// Stack lets a task build on another task's open pull request instead
	// of waiting for it to merge, with g2g keeping the stack's record and
	// its comments on GitHub; see stacks.go.
	Stack bool `json:"stack,omitempty"`
}

const (
	LandBranch = "branch"
	LandPush   = "push"
	// LandPullRequest is the way of a policy with PullRequests on; it is
	// never a Via.
	LandPullRequest = "pull-request"
	ApproveBefore   = "before"
	ApproveNone     = "none"
	// ApprovePM lets the team's PM, not the owner, decide whether a change
	// every checker passed lands. Only a push, which lands straight on the
	// target and nowhere else, offers it: a pull request is governed by
	// GitHub's reviews, and a branch moves nothing.
	ApprovePM = "pm"
	// OpenPM has the team's PM decide whether a pull request opens,
	// OpenOwner the owner, and OpenImplementer leaves it to the implementer,
	// whose passed draft opens one.
	OpenPM          = "pm"
	OpenOwner       = "owner"
	OpenImplementer = "implementer"
)

// Way is how a change lands: through a pull request when they are on, else
// as Via says, defaulting to a new branch.
func (l LandPolicy) Way() string {
	switch {
	case l.PullRequests:
		return LandPullRequest
	case l.Via == "":
		return LandBranch
	}
	return l.Via
}

// Unset reports a policy with nothing in it.
func (l LandPolicy) Unset() bool {
	if len(l.TrustedBots) > 0 {
		return false
	}
	l.TrustedBots = nil
	return reflect.ValueOf(l).IsZero()
}

// TrustsBot reports a login among TrustedBots. GitHub names an app's
// account with a [bot] suffix in some places and without it in others.
func (l LandPolicy) TrustsBot(login string) bool {
	bare := func(s string) string { return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "[bot]")) }
	return login != "" && slices.ContainsFunc(l.TrustedBots, func(b string) bool { return bare(b) == bare(login) })
}

// MergeMethod is how a pull request merges, squash unless set.
func (l LandPolicy) MergeMethod() string {
	if l.Merge == "" {
		return "squash"
	}
	return l.Merge
}

// OpenGate is who decides that a pull request opens, the PM unless set.
func (l LandPolicy) OpenGate() string {
	if l.Open == "" {
		return OpenPM
	}
	return l.Open
}

// MergeGate is who approves a ready pull request merging, the PM unless set.
func (l LandPolicy) MergeGate() string {
	if l.Approve == "" {
		return ApprovePM
	}
	return l.Approve
}

// AsksFirst reports whether someone, the owner or the PM, approves before a
// change goes out: lands, or opens its pull request.
func (l LandPolicy) AsksFirst() bool {
	if l.PullRequests {
		return l.OpenGate() != OpenImplementer
	}
	return l.Approve != ApproveNone
}

// ByPM reports whether the team's PM decides whether a change goes out: a
// push lands, or a pull request opens.
func (l LandPolicy) ByPM() bool {
	if l.PullRequests {
		return l.OpenGate() == OpenPM
	}
	return l.Approve == ApprovePM && l.Way() == LandPush
}

var (
	githubRepo  = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
	githubLogin = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}(?:\[bot\])?$`)
)

// maxTrustedBots keeps the list to the few reviewers a repository runs.
const maxTrustedBots = 20

func (l LandPolicy) validate() error {
	if len(l.Means) > 2000 {
		return errors.New("what landing means must fit in 2000 characters")
	}
	if l.Approve != "" && l.Approve != ApproveBefore && l.Approve != ApproveNone && l.Approve != ApprovePM {
		return errors.New("approve must be before, none or pm")
	}
	if l.Approve == ApprovePM && !l.PullRequests && l.Way() != LandPush {
		return errors.New("the PM can decide only for changes that land by push or pull request: a new branch lands nothing")
	}
	if l.PullRequests {
		if !branchName(l.Target) {
			return errors.New("pull requests need the branch they merge into, such as main")
		}
		if !githubRepo.MatchString(l.GitHub) {
			return errors.New("pull requests need the GitHub repository as owner/name")
		}
		if l.Merge != "" && l.Merge != "squash" && l.Merge != "merge" && l.Merge != "rebase" {
			return errors.New("a pull request merges by squash, merge or rebase")
		}
		if l.Open != "" && l.Open != OpenPM && l.Open != OpenOwner && l.Open != OpenImplementer {
			return errors.New("who opens a pull request is pm, owner or implementer")
		}
		if len(l.TrustedBots) > maxTrustedBots {
			return errors.New("trust at most 20 automated reviewers")
		}
		for _, b := range l.TrustedBots {
			if !githubLogin.MatchString(b) {
				return errors.New("a trusted automated reviewer is named by its GitHub login, such as review-bot[bot]")
			}
		}
	} else if l.GitHub != "" || l.Merge != "" || l.Open != "" {
		return errors.New("a GitHub repository, merge method and who opens are only for pull requests")
	} else if l.Draft || len(l.TrustedBots) > 0 {
		return errors.New("draft pull requests and trusted automated reviewers are only for pull requests")
	} else if l.Stack {
		return errors.New("stacking is only for changes that land by pull request")
	}
	switch l.Via {
	case "", LandBranch:
		// With pull requests on, Target is theirs.
		if l.Method != "" || (l.Target != "" && !l.PullRequests) {
			return errors.New("landing on a new branch takes no target or method")
		}
	case LandPush:
		if !branchName(l.Target) {
			return errors.New("landing by push needs the target branch, such as main")
		}
		if l.Method != "" && l.Method != "fast-forward" {
			return errors.New("a push only lands by fast-forward")
		}
	default:
		return errors.New("landing is via branch or push, with or without pull requests")
	}
	return nil
}

func branchName(name string) bool {
	return name != "" && len(name) <= 200 && !strings.HasPrefix(name, "-") && !strings.HasSuffix(name, "/") && !strings.HasSuffix(name, ".lock") &&
		!strings.ContainsAny(name, " ~^:?*[\\") && !strings.Contains(name, "..") && !strings.Contains(name, "@{")
}

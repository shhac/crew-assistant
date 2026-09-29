package core

import (
	"errors"
	"regexp"
	"strings"
)

// LandPolicy is what "landing" means for a code project: prose for people and
// agents, plus the few fields the loop needs to do it.
type LandPolicy struct {
	Means string `json:"means,omitempty"`
	// Via is LandBranch (a new local branch), LandPush (a fast-forward push
	// onto Target) or LandPullRequest (a GitHub pull request into Target).
	Via    string `json:"via,omitempty"`
	Target string `json:"target,omitempty"`
	// Method is fast-forward for a push, or squash, merge or rebase for a
	// pull request.
	Method string `json:"method,omitempty"`
	// GitHub is the owner/name repository a pull request is opened on.
	GitHub string `json:"github,omitempty"`
	// Approve is ApproveBefore (the owner approves before landing, or before a
	// pull request opens), ApproveNone, or ApprovePM (the team's PM decides
	// whether a signed-off change lands; push only).
	Approve string `json:"approve,omitempty"`
}

const (
	LandBranch      = "branch"
	LandPush        = "push"
	LandPullRequest = "pull-request"
	ApproveBefore   = "before"
	ApproveNone     = "none"
	// ApprovePM lets the team's PM, not the owner, decide whether a change
	// every checker passed lands. Only a push, which lands straight on the
	// target and nowhere else, offers it: a pull request is governed by
	// GitHub's reviews, and a branch moves nothing.
	ApprovePM = "pm"
)

// Way is how a change lands, defaulting to a new branch.
func (l LandPolicy) Way() string {
	if l.Via == "" {
		return LandBranch
	}
	return l.Via
}

// MergeMethod is how a pull request merges, squash unless set.
func (l LandPolicy) MergeMethod() string {
	if l.Method == "" {
		return "squash"
	}
	return l.Method
}

// AsksFirst reports whether someone, the owner or the PM, approves before a
// change lands.
func (l LandPolicy) AsksFirst() bool { return l.Approve != ApproveNone }

// ByPM reports whether the team's PM decides whether a change lands.
func (l LandPolicy) ByPM() bool { return l.Approve == ApprovePM && l.Way() == LandPush }

var githubRepo = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

func (l LandPolicy) validate() error {
	if len(l.Means) > 2000 {
		return errors.New("what landing means must fit in 2000 characters")
	}
	if l.Approve != "" && l.Approve != ApproveBefore && l.Approve != ApproveNone && l.Approve != ApprovePM {
		return errors.New("approve must be before, none or pm")
	}
	if l.Approve == ApprovePM && l.Way() != LandPush {
		return errors.New("the PM can decide only for changes that land by push: a pull request is merged as GitHub's reviews say, and a new branch lands nothing")
	}
	switch l.Way() {
	case LandBranch:
		if l.Target != "" || l.Method != "" || l.GitHub != "" {
			return errors.New("landing on a new branch takes no target, method or github repository")
		}
	case LandPush:
		if !branchName(l.Target) {
			return errors.New("landing by push needs the target branch, such as main")
		}
		if l.Method != "" && l.Method != "fast-forward" {
			return errors.New("a push only lands by fast-forward")
		}
	case LandPullRequest:
		if !branchName(l.Target) {
			return errors.New("a pull request needs the branch it merges into, such as main")
		}
		if !githubRepo.MatchString(l.GitHub) {
			return errors.New("a pull request needs the GitHub repository as owner/name")
		}
		if l.Method != "" && l.Method != "squash" && l.Method != "merge" && l.Method != "rebase" {
			return errors.New("a pull request merges by squash, merge or rebase")
		}
	default:
		return errors.New("landing is via branch, push or pull-request")
	}
	return nil
}

func branchName(name string) bool {
	return name != "" && len(name) <= 200 && !strings.HasPrefix(name, "-") && !strings.HasSuffix(name, "/") && !strings.HasSuffix(name, ".lock") &&
		!strings.ContainsAny(name, " ~^:?*[\\") && !strings.Contains(name, "..") && !strings.Contains(name, "@{")
}

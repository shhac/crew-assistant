package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/shhac/crew-assistant/internal/releaseversion"
)

var ErrVersionTaken = errors.New("the version tag already points at another commit")

// ExitStatus is a native git failure's status, or -1 for a refused operation.
func ExitStatus(err error) int {
	var e *gitError
	if errors.As(err, &e) {
		return e.code
	}
	return -1
}

// RecoverReleaseTag checks the destination before any branch push, and brings
// back an existing annotation after the project's clone was rebuilt.
func (r Repo) RecoverReleaseTag(ctx context.Context, url, version, commit string, config []string) error {
	at, err := r.RemoteTag(ctx, url, version, config)
	if err != nil {
		return err
	}
	if at == "" {
		return nil
	}
	if at != commit {
		return ErrVersionTaken
	}
	local, err := r.tagAt(ctx, version)
	if err != nil {
		return err
	}
	if local != "" {
		if local != commit {
			return ErrVersionTaken
		}
		return nil
	}
	args := append(append([]string(nil), config...), fetchQuietly...)
	_, err = run(ctx, r.Workspace(), append(args, url, "refs/tags/"+version+":refs/tags/"+version)...)
	return err
}

// FetchVersionTags uses a private namespace so upstream tags never overwrite
// an annotated tag this daemon is in the middle of publishing.
func (r Repo) FetchVersionTags(ctx context.Context, from string, config []string) error {
	if from == "" {
		from = r.source
	}
	args := append(append([]string(nil), config...), fetchQuietly...)
	_, err := run(ctx, r.Workspace(), append(args, "--prune", from, "+refs/tags/*:refs/crew-assistant/tags/*")...)
	return err
}
func (r Repo) LatestVersionTag(ctx context.Context, tip string) (string, error) {
	return r.latestVersionTag(ctx, tip, "refs/crew-assistant/tags/")
}

// HighestRemoteVersion includes occupied versions even outside the target.
func (r Repo) HighestRemoteVersion(ctx context.Context, url string, config []string) (string, error) {
	namespace := "refs/crew-assistant/release-remote-tags/"
	args := append(append([]string(nil), config...), fetchQuietly...)
	if _, err := run(ctx, r.Workspace(), append(args, "--prune", url, "+refs/tags/*:"+namespace+"*")...); err != nil {
		return "", err
	}
	return r.latestVersionTag(ctx, "", namespace)
}
func (r Repo) latestVersionTag(ctx context.Context, tip, namespace string) (string, error) {
	args := []string{"for-each-ref", "--format=%(refname:strip=3)", namespace}
	if tip != "" {
		args = append(args, "--merged="+tip)
	}
	out, err := run(ctx, r.Workspace(), args...)
	if err != nil {
		return "", err
	}
	latest := ""
	for _, tag := range strings.Fields(out) {
		if releaseversion.Valid(tag) && (latest == "" || releaseversion.Compare(tag, latest) > 0) {
			latest = tag
		}
	}
	return latest, nil
}
func (r Repo) CommitsSince(ctx context.Context, tag, tip string) ([]string, int, error) {
	span := tip
	if tag != "" {
		span = "refs/crew-assistant/tags/" + tag + ".." + tip
	}
	count, err := run(ctx, r.Workspace(), "rev-list", "--first-parent", "--count", span)
	if err != nil {
		return nil, 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(count))
	if err != nil {
		return nil, 0, err
	}
	out, err := run(ctx, r.Workspace(), "log", "--first-parent", "-50", "--format=%h %s", span)
	var lines []string
	if strings.TrimSpace(out) != "" {
		lines = strings.Split(strings.TrimSpace(out), "\n")
	}
	return lines, n, err
}

// RemoteTag distinguishes absence from a failed lookup and peels annotations.
func (r Repo) RemoteTag(ctx context.Context, url, version string, config []string) (string, error) {
	args := append(append([]string(nil), config...), "ls-remote", "--tags", url, "refs/tags/"+version, "refs/tags/"+version+"^{}")
	out, err := run(ctx, r.Workspace(), args...)
	if err != nil {
		return "", err
	}
	object, commit := "", ""
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if strings.HasSuffix(f[1], "^{}") {
			commit = f[0]
		} else {
			object = f[0]
		}
	}
	if commit != "" {
		return commit, nil
	}
	return object, nil
}
func (r Repo) tagAt(ctx context.Context, version string) (string, error) {
	_, err := run(ctx, r.Workspace(), "show-ref", "--verify", "--quiet", "refs/tags/"+version)
	var e *gitError
	if errors.As(err, &e) && e.code == 1 {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	out, err := run(ctx, r.Workspace(), "rev-parse", "refs/tags/"+version+"^{commit}")
	return strings.TrimSpace(out), err
}

// MakeTag keeps the same annotation object on a reconciled publication.
func (r Repo) MakeTag(ctx context.Context, commit, version, notes string) error {
	if !releaseversion.Valid(version) || !r.Holds(ctx, commit) {
		return errors.New("invalid release version or commit")
	}
	at, err := r.tagAt(ctx, version)
	if err != nil {
		return err
	}
	if at != "" {
		if at == commit {
			return nil
		}
		return ErrVersionTaken
	}
	// A rebuilt clone can recover the daemon's original local annotation.
	source, err := r.RemoteTag(ctx, r.source, version, nil)
	if err != nil {
		return err
	}
	if source != "" {
		if source != commit {
			return ErrVersionTaken
		}
		_, err = run(ctx, r.Workspace(), append(fetchQuietly, r.source, "refs/tags/"+version+":refs/tags/"+version)...)
		return err
	}
	sign, config, err := r.signing(ctx)
	if err != nil {
		return err
	}
	identity, err := r.identity(ctx)
	if err != nil {
		return err
	}
	env := append(gitEnvironment(), identity...)
	env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(config)))
	for i, kv := range config {
		n := strconv.Itoa(i)
		env = append(env, "GIT_CONFIG_KEY_"+n+"="+kv[0], "GIT_CONFIG_VALUE_"+n+"="+kv[1])
	}
	flag := "--no-sign"
	if sign {
		flag = "--sign"
	}
	_, err = runIn(ctx, r.Workspace(), env, "-c", "tag.gpgSign=false", "tag", "--annotate", flag, "--message", notes, version, commit)
	return err
}
func (r Repo) TagSource(ctx context.Context, version string) error {
	return r.pushTag(ctx, r.source, version, nil, true)
}
func (r Repo) PushTag(ctx context.Context, url, version string, config []string) error {
	return r.pushTag(ctx, url, version, config, false)
}
func (r Repo) pushTag(ctx context.Context, url, version string, config []string, local bool) error {
	commit, err := r.tagAt(ctx, version)
	if err != nil {
		return err
	}
	if commit == "" {
		return errors.New("release tag is missing")
	}
	at, err := r.RemoteTag(ctx, url, version, config)
	if err != nil {
		return err
	}
	if at != "" {
		if at == commit {
			return nil
		}
		return ErrVersionTaken
	}
	args := append(append([]string(nil), config...), "push", "--porcelain", "--no-verify")
	if local {
		args = append(args, "--receive-pack="+receivePack)
	}
	_, err = run(ctx, r.Workspace(), append(args, url, "refs/tags/"+version+":refs/tags/"+version)...)
	return err
}

// PushReleaseBranch pushes only the checked commit, never the moving local tip.
func (r Repo) PushReleaseBranch(ctx context.Context, url, commit, target string, config []string) error {
	remote, err := r.FetchFrom(ctx, url, target, config)
	if err != nil {
		return err
	}
	contains, err := r.Contains(ctx, remote, commit)
	if err != nil {
		return err
	}
	if contains {
		return nil
	}
	ancestor, err := r.Contains(ctx, commit, remote)
	if err != nil {
		return err
	}
	if !ancestor {
		return ErrTargetMoved
	}
	args := append(append([]string(nil), config...), "push", "--porcelain", "--no-verify", url, commit+":refs/heads/"+target)
	out, err := run(ctx, r.Workspace(), args...)
	if err != nil {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "!") && (strings.Contains(line, "non-fast-forward") || strings.Contains(line, "fetch first")) {
				return ErrTargetMoved
			}
		}
		return fmt.Errorf("release branch push: %w", err)
	}
	return nil
}

// DiscardUnpublishedTag replaces only an abandoned clone tag, never a published tag.
func (r Repo) DiscardUnpublishedTag(ctx context.Context, version, commit, destination string, config []string) error {
	local, err := r.tagAt(ctx, version)
	if err != nil || local == "" || local == commit {
		return err
	}
	source, err := r.RemoteTag(ctx, r.source, version, nil)
	if err != nil {
		return err
	}
	if source != "" {
		return ErrVersionTaken
	}
	if destination != "" {
		remote, err := r.RemoteTag(ctx, destination, version, config)
		if err != nil {
			return err
		}
		if remote != "" {
			return ErrVersionTaken
		}
	}
	ref := "refs/tags/" + version
	object, err := run(ctx, r.Workspace(), "rev-parse", ref)
	if err != nil {
		return err
	}
	_, err = run(ctx, r.Workspace(), "update-ref", "-d", ref, strings.TrimSpace(object))
	return err
}

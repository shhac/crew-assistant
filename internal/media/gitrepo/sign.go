package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Signing is whether the daemon's commits are signed.
type Signing int

const (
	// SignAsOwner signs exactly when the owner's own commits in the
	// repository would be: their global, system and repository config decide.
	SignAsOwner Signing = iota
	SignAlways
	SignNever
)

// commit runs a git command that writes a commit (commit or commit-tree),
// signed or not as the project says.
func (r Repo) commit(ctx context.Context, command string, args ...string) (string, error) {
	sign, config, err := r.signing(ctx)
	if err != nil {
		return "", err
	}
	flag := "--no-gpg-sign"
	if sign {
		flag = "-S"
	}
	env := gitEnvironment()
	if len(config) > 0 {
		env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(config)))
		for i, kv := range config {
			n := strconv.Itoa(i)
			env = append(env, "GIT_CONFIG_KEY_"+n+"="+kv[0], "GIT_CONFIG_VALUE_"+n+"="+kv[1])
		}
	}
	out, err := runIn(ctx, r.Workspace(), env, append([]string{command, flag}, args...)...)
	if err != nil && sign {
		return out, fmt.Errorf("%w (signing is on for this project: fix the signing setup, or set the team's signing to never)", err)
	}
	return out, err
}

// signing reads, from the owner's configuration for the source repository,
// whether to sign and the settings that say how: the key, the format and the
// signing program. The daemon otherwise ignores the owner's configuration, but
// the signing program is one the owner's own commits here already run.
func (r Repo) signing(ctx context.Context) (bool, [][2]string, error) {
	if r.sign == SignNever {
		return false, nil, nil
	}
	sign := r.sign == SignAlways
	if !sign {
		out, err := ownerConfig(ctx, r.source, "--type=bool", "--get", "commit.gpgsign")
		if err != nil {
			return false, nil, err
		}
		sign = strings.TrimSpace(out) == "true"
	}
	if !sign {
		return false, nil, nil
	}
	out, err := ownerConfig(ctx, r.source, "-z", "--get-regexp", `^(user\.(signingkey|name|email)|gpg\..*)$`)
	if err != nil {
		return false, nil, err
	}
	var config [][2]string
	identity := map[string]string{}
	for _, entry := range strings.Split(out, "\x00") {
		key, value, _ := strings.Cut(entry, "\n")
		switch key {
		case "":
		case "user.name", "user.email":
			identity[key] = value
		default:
			config = append(config, [2]string{key, value})
		}
	}
	// Without a signing key git signs as the committer, which in the clone is
	// the daemon; the owner's key is the one their own commits would use.
	if !hasKey(config, "user.signingkey") && identity["user.email"] != "" {
		config = append(config, [2]string{"user.signingkey", strings.TrimSpace(identity["user.name"] + " <" + identity["user.email"] + ">")})
	}
	return true, config, nil
}

func hasKey(config [][2]string, key string) bool {
	for _, kv := range config {
		if kv[0] == key {
			return true
		}
	}
	return false
}

// ownerConfig reads git config for dir as the owner's own git would, where
// they chose their global and system files. Nothing is found is not an error.
func ownerConfig(ctx context.Context, dir string, args ...string) (string, error) {
	env := gitEnvironment()
	for _, name := range []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM"} {
		env = unset(env, name)
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	out, err := runIn(ctx, dir, env, append([]string{"config"}, args...)...)
	var status *gitError
	if errors.As(err, &status) && status.code == 1 {
		return "", nil
	}
	return out, err
}

func unset(env []string, name string) []string {
	out := env[:0:0]
	for _, entry := range env {
		if !strings.HasPrefix(entry, name+"=") {
			out = append(out, entry)
		}
	}
	return out
}

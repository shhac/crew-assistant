package managedworkers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/statepath"
)

// Base images contain only general-purpose tools. No project directory, Docker
// credential store or host package-manager config is a build context.
const toolchainDockerfile = `FROM golang:1.26-bookworm AS go
FROM node:24-bookworm
COPY --from=go /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}" GOPATH=/tmp/go GOCACHE=/tmp/go-build GOTOOLCHAIN=local
WORKDIR /workspace
`
const toolchainTag = "agent-assistant-worker:go1.26-node24-v1"

var imageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type commandRunner interface {
	Run(context.Context, string, []string, []byte, []string, string) ([]byte, error)
}
type localRuntime struct {
	command  commandRunner
	lookPath func(string) (string, error)
	home     string
	platform string
	exists   func(string) bool
}

func newLocalRuntime() *localRuntime {
	home, _ := os.UserHomeDir()
	return &localRuntime{command: hostCommand{}, lookPath: exec.LookPath, home: home, platform: runtime.GOOS, exists: func(path string) bool { _, err := os.Stat(path); return err == nil }}
}
func (r *localRuntime) binary(name string) (string, error) {
	if path, err := r.lookPath(name); err == nil {
		return path, nil
	}
	if r.platform == "darwin" {
		for _, prefix := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
			path := filepath.Join(prefix, name)
			if r.exists(path) {
				return path, nil
			}
		}
	}
	return "", errors.New("required local tool is unavailable")
}
func (r *localRuntime) private(root string) (string, error) {
	dir, err := statepath.EnsureDirectory(root, "runtime", "docker-config")
	if err != nil {
		return "", err
	}
	// Docker searches these standard Homebrew plugin directories without reading
	// the user's Docker credential or context configuration.
	if r.platform == "darwin" {
		raw := []byte(`{"cliPluginsExtraDirs":["/opt/homebrew/lib/docker/cli-plugins","/usr/local/lib/docker/cli-plugins"]}`)
		filename := filepath.Join(dir, "config.json")
		if _, statErr := os.Stat(filename); os.IsNotExist(statErr) {
			if err = os.WriteFile(filename, raw, 0600); err != nil {
				return "", err
			}
		} else if statErr != nil {
			return "", statErr
		}
	}
	return dir, nil
}
func (r *localRuntime) run(ctx context.Context, root, bin string, args []string, input []byte, home bool) ([]byte, error) {
	searchPath := os.Getenv("PATH")
	if r.platform == "darwin" {
		searchPath += ":/opt/homebrew/bin:/usr/local/bin"
	}
	env := []string{"PATH=" + searchPath, "LANG=C.UTF-8"}
	if home {
		env = append(env, "HOME="+r.home, "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ANALYTICS=1", "HOMEBREW_NO_ENV_HINTS=1", "NONINTERACTIVE=1")
	}
	return r.command.Run(ctx, bin, args, input, env, root)
}
func (r *localRuntime) docker(ctx context.Context, root, socket string, args []string, input []byte) ([]byte, error) {
	if !localSocket(socket) {
		return nil, errors.New("worker isolation requires a local connection")
	}
	bin, err := r.binary("docker")
	if err != nil {
		return nil, errors.New("local worker tools are missing; ask the assistant to prepare the worker again")
	}
	dir, err := r.private(root)
	if err != nil {
		return nil, err
	}
	argv := append([]string{"--host", "unix://" + socket, "--config", dir}, args...)
	return r.run(ctx, root, bin, argv, input, false)
}
func localSocket(path string) bool {
	return filepath.IsAbs(path) && !strings.ContainsAny(path, "\x00\r\n") && !strings.Contains(path, "://")
}
func (r *localRuntime) ready(ctx context.Context, root, socket string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := r.docker(ctx, root, socket, []string{"info", "--format", "{{.ServerVersion}}"}, nil)
	return err == nil
}
func (r *localRuntime) Check(ctx context.Context, root string, env environment) error {
	if !localSocket(env.Socket) || !imageID.MatchString(env.Image) {
		return errors.New("saved worker environment is invalid; preserve it for recovery")
	}
	if !r.ready(ctx, root, env.Socket) {
		return errors.New("the local worker environment is asleep or unavailable; ask the assistant to prepare it again")
	}
	data, err := r.docker(ctx, root, env.Socket, []string{"image", "inspect", "--format", "{{.Id}}", env.Image}, nil)
	if err != nil || strings.TrimSpace(string(data)) != env.Image {
		return errors.New("the worker toolchain is missing; ask the assistant to prepare it again")
	}
	return nil
}
func (r *localRuntime) Prepare(ctx context.Context, root string, previous *environment) (environment, error) {
	var env environment
	if r.platform != "darwin" && r.platform != "linux" {
		return env, errors.New("automatic worker setup currently needs a Mac or Linux computer; use a supported computer for this project's worker")
	}
	if r.home == "" {
		return env, errors.New("the assistant cannot find this computer's user account for worker setup")
	}
	if _, err := r.private(root); err != nil {
		return env, err
	}
	if previous != nil {
		if !localSocket(previous.Socket) || !imageID.MatchString(previous.Image) {
			return env, errors.New("saved worker environment is invalid; preserve it for recovery")
		}
		env = *previous
		if r.Check(ctx, root, env) == nil {
			return env, nil
		}
	}
	if _, err := r.binary("docker"); err != nil {
		if err = r.install(ctx, root); err != nil {
			return env, err
		}
	}
	if env.Socket == "" {
		env.Socket = r.discover(ctx, root)
	}
	if env.Socket == "" || !r.ready(ctx, root, env.Socket) {
		if r.platform != "darwin" {
			return env, errors.New("this computer's local isolation service is unavailable; start it or ask an administrator to enable local containers, then retry worker setup")
		}
		dedicated := filepath.Join(r.home, ".colima", "agent-assistant", "docker.sock")
		if env.Socket != "" && env.Socket != dedicated {
			return env, errors.New("this project's existing local isolation service has stopped; open its desktop application, then retry worker setup")
		}
		if _, err := r.binary("colima"); err != nil {
			if err = r.install(ctx, root); err != nil {
				return env, err
			}
		}
		bin, err := r.binary("colima")
		if err != nil {
			return env, errors.New("worker tools did not finish installing; retry setup after the package manager has finished")
		}
		if strings.ContainsAny(root, ":,\r\n") {
			return env, errors.New("the assistant state folder contains characters unsupported by automatic worker setup; choose a simpler state folder")
		}
		args := []string{"start", "--profile", "agent-assistant", "--runtime", "docker", "--activate=false", "--ssh-config=false", "--template=false", "--vm-type", "vz", "--cpus", "2", "--memory", "4", "--disk", "30", "--mount", root + ":w"}
		if _, err = r.run(ctx, root, bin, args, nil, true); err != nil {
			return env, errors.New("the computer could not start its private worker environment; retry setup, or check that virtualization is available")
		}
		env.Socket = dedicated
		if !r.ready(ctx, root, env.Socket) {
			return env, errors.New("the private worker environment is still starting; retry setup shortly")
		}
	}
	// A saved image remains immutable if it is still present. Builds happen only
	// on explicit preparation; reconnecting a daemon never triggers downloads.
	if env.Image != "" {
		if data, err := r.docker(ctx, root, env.Socket, []string{"image", "inspect", "--format", "{{.Id}}", env.Image}, nil); err == nil && strings.TrimSpace(string(data)) == env.Image {
			return env, nil
		}
	}
	if data, err := r.docker(ctx, root, env.Socket, []string{"image", "inspect", "--format", "{{.Id}}", toolchainTag}, nil); err == nil && imageID.MatchString(strings.TrimSpace(string(data))) {
		env.Image = strings.TrimSpace(string(data))
		return env, nil
	}
	if _, err := r.docker(ctx, root, env.Socket, []string{"buildx", "version"}, nil); err != nil {
		if err = r.install(ctx, root); err != nil {
			return env, err
		}
	}
	if _, err := r.docker(ctx, root, env.Socket, []string{"build", "--pull", "--tag", toolchainTag, "-"}, []byte(toolchainDockerfile)); err != nil {
		return env, errors.New("worker tools could not be downloaded; check the computer's internet connection and retry setup")
	}
	data, err := r.docker(ctx, root, env.Socket, []string{"image", "inspect", "--format", "{{.Id}}", toolchainTag}, nil)
	if err != nil || !imageID.MatchString(strings.TrimSpace(string(data))) {
		return env, errors.New("the downloaded worker toolchain could not be verified; retry setup")
	}
	env.Image = strings.TrimSpace(string(data))
	return env, nil
}
func (r *localRuntime) install(ctx context.Context, root string) error {
	if r.platform != "darwin" {
		return errors.New("this computer needs its local container tools installed by an administrator before the assistant can prepare workers")
	}
	brew, err := r.binary("brew")
	if err != nil {
		return errors.New("automatic worker setup needs Homebrew on this Mac; install Homebrew once, then ask the assistant to finish setup")
	}
	// Never install casks, evaluate scripts or accept package names from a model.
	if _, err = r.run(ctx, root, brew, []string{"install", "docker", "docker-buildx", "colima"}, nil, true); err != nil {
		return errors.New("the free worker tools could not finish installing; finish any computer setup requested by Homebrew, then retry")
	}
	return nil
}
func (r *localRuntime) discover(ctx context.Context, root string) string {
	candidates := []string{filepath.Join(r.home, ".colima", "agent-assistant", "docker.sock"), filepath.Join(r.home, ".docker", "run", "docker.sock"), "/var/run/docker.sock"}
	// Read only the selected context's endpoint metadata. Never inherit its
	// credentials or DOCKER_HOST/DOCKER_CONTEXT when connecting to the daemon.
	if bin, err := r.binary("docker"); err == nil {
		probe, cancel := context.WithTimeout(ctx, 10*time.Second)
		data, e := r.run(probe, root, bin, []string{"context", "inspect", "--format", "{{json .Endpoints.docker.Host}}"}, nil, true)
		cancel()
		var endpoint string
		if e == nil && json.Unmarshal(data, &endpoint) == nil && strings.HasPrefix(endpoint, "unix://") && localSocket(strings.TrimPrefix(endpoint, "unix://")) {
			candidates = append(candidates, strings.TrimPrefix(endpoint, "unix://"))
		}
	}
	seen := map[string]bool{}
	for _, socket := range candidates {
		if !seen[socket] && r.exists(socket) {
			seen[socket] = true
			if r.ready(ctx, root, socket) {
				return socket
			}
		}
	}
	return ""
}

// Details are deliberately excluded from error strings: subprocess output can
// contain host paths or third-party messages. Only bounded fixed probes return it.
func commandError(err error) error {
	if err != nil {
		return fmt.Errorf("local worker setup command failed")
	}
	return nil
}

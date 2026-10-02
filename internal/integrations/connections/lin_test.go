package connections

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
)

func fakeLin(t *testing.T, body string) (Client, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nif [ \"$1\" = auth ]; then printf '%s\\n' '{\"alias\":\"home\"}'; exit; fi\nprintf '%s\\n' \"$@\" > '" + log + "'\nprintf '%s\\n' \"identity=$LIN_REQUIRE_IDENTITY key=$LINEAR_API_KEY\" >> '" + log + "'\n" + body
	if err := os.WriteFile(filepath.Join(dir, "lin"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LINEAR_API_KEY", "secret-not-inherited")
	return New(), log
}
func linBindings(writes bool) []config.Connection {
	return []config.Connection{{ID: "linear", Tool: "lin", Profiles: []string{"home"}, AllowWrites: writes}}
}
func TestLinExecProfileLiteralArgsAndEnvironment(t *testing.T) {
	c, log := fakeLin(t, "printf '{\"title\":\"data\"}'\n")
	marker := filepath.Join(t.TempDir(), "pwned")
	args := []string{"issue", "search", "$(touch " + marker + "); 'a b'\nmore"}
	out, err := c.Lin(context.Background(), linBindings(false), LinCall{ConnectionID: "linear", Profile: "home", Args: args})
	if err != nil || out.Failed {
		t.Fatalf("%+v %v", out, err)
	}
	data, _ := os.ReadFile(log)
	want := strings.Join(append(append([]string{"--color", "never", "--workspace", "home"}, args...), "identity=1 key="), "\n") + "\n"
	if string(data) != want {
		t.Fatalf("argv/env: %q wanted %q", data, want)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("shell expanded arguments")
	}
	if !strings.Contains(out.Notice, "not instructions") {
		t.Fatal(out)
	}
}
func TestLinClipsAndReportsPartialFailure(t *testing.T) {
	c, _ := fakeLin(t, "head -c 204800 /dev/zero | tr '\\000' x\nprintf '%s' '{\"error\":\"lin_api_SECRET rejected\",\"hint\":\"sign in again\"}' >&2\nexit 1\n")
	out, err := c.Lin(context.Background(), linBindings(false), LinCall{ConnectionID: "linear", Profile: "home", Args: []string{"issue", "list"}})
	if err != nil || !out.Clipped || !out.Failed || !strings.Contains(out.Output, "[clipped: 172032 bytes more") || len(out.Output) > 33000 || strings.Contains(out.Error, "SECRET") || !strings.Contains(out.Error, "sign in again") {
		t.Fatalf("result: %+v error=%v", out, err)
	}
}
func TestLinPolicyAndUnavailableProfiles(t *testing.T) {
	for _, path := range linReads {
		if _, err := LinValidate(strings.Split(path, "/"), false); err != nil {
			t.Errorf("read %s: %v", path, err)
		}
	}
	for _, path := range linWrites {
		args := strings.Split(path, "/")
		if _, err := LinValidate(args, false); err == nil {
			t.Errorf("write %s allowed", path)
		}
		if write, err := LinValidate(args, true); err != nil || !write {
			t.Errorf("opted-in write %s: %v", path, err)
		}
	}
	denied := [][]string{{"lin", "issue", "get"}, {"api", "query"}, {"auth", "status"}, {"config", "get"}, {"file", "upload"}, {"mcp"}, {"issue", "delete"}, {"issue", "archive"}, {"project", "unarchive"}, {"issue", "relation", "remove"}, {"issue", "attachment", "remove"}, slices.Repeat([]string{"x"}, 33), {"issue", "get", "x\x00"}}
	for _, flag := range []string{"--file", "--file=x", "--workspace=x", "--workspace", "--debug", "-d", "-w", "--format=pretty", "--format"} {
		denied = append(denied, []string{"issue", "get", flag, "pretty"})
	}
	for _, args := range denied {
		if _, err := LinValidate(args, true); err == nil {
			t.Errorf("allowed %v", args)
		}
	}
	c, log := fakeLin(t, "exit 0")
	for _, profile := range []string{"elsewhere", "gone"} {
		b := linBindings(false)
		if profile == "gone" {
			b[0].Profiles = append(b[0].Profiles, profile)
		}
		out, err := c.Lin(context.Background(), b, LinCall{ConnectionID: "linear", Profile: profile, Args: []string{"issue", "list"}})
		if err == nil && !out.Failed {
			t.Fatal("unknown profile ran")
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("command ran despite unavailable profile")
	}
}
func TestLinTimeoutWriteOutcomeUnknown(t *testing.T) {
	c, _ := fakeLin(t, "sleep 10\n")
	c.RunClipped = func(ctx context.Context, name string, args []string) LinResult {
		ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		return runClipped(ctx, name, args)
	}
	ctx := context.Background()
	out, err := c.Lin(ctx, linBindings(true), LinCall{ConnectionID: "linear", Profile: "home", Args: []string{"issue", "comment", "new", "ENG-1", "body"}, Writes: true})
	if err != nil || !out.Failed || !out.Unknown || !strings.Contains(out.Error, "outcome unknown") {
		t.Fatalf("%+v %v", out, err)
	}
}
func TestLinConnectionPermissionCannotBeOverridden(t *testing.T) {
	c, _ := fakeLin(t, "exit 0")
	if _, err := c.Lin(context.Background(), linBindings(false), LinCall{ConnectionID: "linear", Profile: "home", Args: []string{"issue", "new", "title"}, Writes: true}); err == nil {
		t.Fatal("caller granted writes")
	}
}

func TestLinActivitySummaryNeverIncludesBodiesOrFlagValues(t *testing.T) {
	for _, args := range [][]string{
		{"issue", "new", "ENG-99"},
		{"issue", "comment", "new", "ENG-1", "ENG-99"},
		{"issue", "update", "description", "ENG-1", "ENG-99"},
		{"issue", "attachment", "add", "ENG-1", "https://example.com", "--title", "ENG-99"},
	} {
		if summary := LinCommandPath(args); strings.Contains(summary, "ENG-99") {
			t.Fatalf("body or flag leaked: %s", summary)
		}
	}
}
func TestLinUsageUsesFilteredReferenceWithoutCLI(t *testing.T) {
	c := Client{Run: func(context.Context, string, []string) ([]byte, error) { t.Fatal("usage ran CLI"); return nil, nil }}
	out, err := c.Lin(context.Background(), linBindings(false), LinCall{ConnectionID: "linear", Profile: "home", Args: []string{"issue", "usage"}})
	if err != nil || !strings.Contains(out.Output, "issue search") || strings.Contains(out.Output, "issue new") {
		t.Fatalf("usage: %v", err)
	}
}

func TestLinSuccessfulWriteRedactsStdout(t *testing.T) {
	c, _ := fakeLin(t, "printf '%s' '{\"id\":\"comment\",\"body\":\"lin_oauth_SECRET\"}'\n")
	out, err := c.Lin(context.Background(), linBindings(true), LinCall{ConnectionID: "linear", Profile: "home", Args: []string{"issue", "comment", "new", "ENG-1", "hello"}, Writes: true})
	if err != nil || out.Failed || !strings.Contains(out.Output, "[redacted]") || strings.Contains(out.Output, "SECRET") {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestLinGlobalFlagsPrecedeDocumentSubcommand(t *testing.T) {
	c, log := fakeLin(t, "printf '{}'")
	args := []string{"document", "new", "Title", "--content", "body"}
	out, err := c.Lin(context.Background(), linBindings(true), LinCall{ConnectionID: "linear", Profile: "home", Args: args, Writes: true})
	if err != nil || out.Failed {
		t.Fatalf("%+v %v", out, err)
	}
	data, _ := os.ReadFile(log)
	want := strings.Join(append([]string{"--color", "never", "--workspace", "home"}, args...), "\n") + "\nidentity=1 key=\n"
	if string(data) != want {
		t.Fatalf("argv %q wanted %q", data, want)
	}
}
func TestLinCallerCancellationIsNotADeadline(t *testing.T) {
	c, _ := fakeLin(t, "sleep 10")
	c.RunClipped = func(ctx context.Context, name string, args []string) LinResult {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		return runClipped(ctx, name, args)
	}
	out, err := c.Lin(context.Background(), linBindings(true), LinCall{ConnectionID: "linear", Profile: "home", Args: []string{"issue", "comment", "new", "ENG-1", "body"}, Writes: true})
	if err != nil || !out.Unknown || !out.Failed || !strings.Contains(out.Error, "cancelled") || strings.Contains(out.Error, "30s") {
		t.Fatalf("%+v %v", out, err)
	}
}
func TestLinFlagsCoverAllowedReferenceCommands(t *testing.T) {
	data, _ := linSkill.ReadFile("lin_skill/references/commands.md")
	for _, line := range strings.Split(string(data), "\n") {
		matches := guidanceCommands.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			continue
		}
		path, _ := linPath(strings.Fields(matches[0][1]))
		if path == "" {
			continue
		}
		for _, flag := range guidanceFlags.FindAllString(matches[0][1], -1) {
			if slices.Contains([]string{"--file", "--color"}, flag) {
				continue
			} // always-refused file and global appearance flags
			args := append(strings.Split(path, "/"), flag, "json")
			if _, err := LinValidate(args, true); err != nil {
				t.Errorf("documented flag %s on %s: %v", flag, path, err)
			}
		}
	}
}

//go:build !windows

package cli

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func upgradeTestListener(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if errors.Is(err, syscall.EPERM) && os.Getenv("CREW_REQUIRE_UPGRADE_LOOPBACK") != "1" {
		t.Skip("loopback bind denied; set CREW_REQUIRE_UPGRADE_LOOPBACK=1 to require this test")
	}
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestPreservedListenerAcceptsAndShutsDown(t *testing.T) {
	l := upgradeTestListener(t)
	defer l.Close()
	f, err := preserveUpgradeListener(l)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if upgradeListenerDescriptor(f) == "" {
		t.Fatal("missing descriptor")
	}
	httpServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })}
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(l) }()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	// Let Serve enter its next Accept, which must remain pollable.
	time.Sleep(20 * time.Millisecond)
	shutdown := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		shutdown <- httpServer.Shutdown(ctx)
	}()
	select {
	case err := <-shutdown:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("preservation made Accept block shutdown")
	}
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}

func TestUpgradeListenerSurvivesReexecOnSameAddress(t *testing.T) {
	l := upgradeTestListener(t)
	defer l.Close()
	f, err := l.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestUpgradeListenerHelper$")
	cmd.ExtraFiles = []*os.File{f}
	cmd.Env = append(os.Environ(), "CREW_TEST_UPGRADE_HELPER=preserve", upgradeListenerEnv+"=3")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != l.Addr().String() {
		t.Fatal(line, err)
	}
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + l.Addr().String() + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "healthy after exec" {
		t.Fatal(string(body))
	}
}

func TestUpgradeListenerHelper(t *testing.T) {
	stage := os.Getenv("CREW_TEST_UPGRADE_HELPER")
	if stage == "" {
		return
	}
	l, err := inheritedUpgradeListener()
	if err != nil || l == nil {
		t.Fatal(l, err)
	}
	defer l.Close()
	if os.Getenv(upgradeListenerEnv) != "" {
		t.Fatal("descriptor environment not consumed")
	}
	if stage == "preserve" {
		f, err := preserveUpgradeListener(l)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		_ = os.Setenv(upgradeListenerEnv, fmtFD(f))
		_ = os.Setenv("CREW_TEST_UPGRADE_HELPER", "serve")
		if err = replaceUpgradeProcess(os.Args[0], []string{"-test.run=^TestUpgradeListenerHelper$"}); err != nil {
			t.Fatal(err)
		}
		t.Fatal("exec returned")
	}
	printlnAddress := l.Addr().String() + "\n"
	_, _ = os.Stdout.WriteString(printlnAddress)
	_ = http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("healthy after exec")) }))
}

func fmtFD(f *os.File) string { return upgradeListenerDescriptor(f) }

// This starts the production detached launcher while the preserved socket is
// open, then proves the child cannot keep it bound after the parent closes it.
func TestDetachedUpgradeChildDoesNotHoldListener(t *testing.T) {
	l := upgradeTestListener(t)
	address := l.Addr().String()
	f, err := preserveUpgradeListener(l)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	defer l.Close()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	release := filepath.Join(dir, "release")
	t.Setenv("CREW_DETACHED_LISTENER_TEST", dir)
	t.Setenv(upgradeListenerEnv, fmtFD(f))
	if err := startUpgradeDetached(os.Args[0], []string{"-test.run=^TestDetachedUpgradeListenerHelper$"}, filepath.Join(dir, "helper.log")); err != nil {
		t.Fatal(err)
	}
	defer os.WriteFile(release, []byte("stop"), 0600)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatal("child never started", err)
	}
	l.Close()
	f.Close()
	rebound, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal("detached child retained listener", err)
	}
	rebound.Close()
}
func TestDetachedUpgradeListenerHelper(t *testing.T) {
	dir := os.Getenv("CREW_DETACHED_LISTENER_TEST")
	if dir == "" {
		return
	}
	if os.Getenv(upgradeListenerEnv) != "" {
		t.Fatal("listener environment inherited")
	}
	if err := os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

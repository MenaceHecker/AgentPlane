package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MenaceHecker/AgentPlane/mockmodel"
)

// buildBinary compiles the command once per test into a temporary directory
// (removed by the testing framework). It uses only the local Go toolchain.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mockmodel")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}
	return bin
}

// A bad configuration must exit nonzero with a clear message BEFORE serving:
// nothing may be listening and no "listening" line may be printed.
func TestBinaryRejectsInvalidConfigurationBeforeServing(t *testing.T) {
	bin := buildBinary(t)
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"error rate 2", []string{"--error-rate=2"}, "error-rate"},
		{"negative completion tokens", []string{"--completion-tokens=-1"}, "completion-tokens"},
		{"latency over the cap", []string{"--latency=1h"}, "latency"},
		{"unknown flag", []string{"--tenant=acme"}, "tenant"},
		{"bad listen address", []string{"--listen=nowhere"}, "listen"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, tt.args...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("expected a nonzero exit, got %v", err)
			}
			if ee.ExitCode() != 2 {
				t.Errorf("exit code = %d, want 2 for a configuration error", ee.ExitCode())
			}
			if !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr %q does not mention %q", stderr.String(), tt.wantErr)
			}
			if stdout.Len() != 0 {
				t.Errorf("the server printed %q, so it started despite the bad configuration", stdout.String())
			}
		})
	}
}

func TestBinaryHelpExitsZero(t *testing.T) {
	bin := buildBinary(t)
	out, err := exec.Command(bin, "-h").CombinedOutput()
	if err != nil {
		t.Fatalf("-h must exit 0, got %v\n%s", err, out)
	}
	for _, f := range []string{"listen", "latency", "prompt-tokens", "completion-tokens", "error-rate", "seed"} {
		if !strings.Contains(string(out), f) {
			t.Errorf("usage does not mention %s", f)
		}
	}
}

// The binary must serve, answer deterministically and exit cleanly (status 0)
// on SIGTERM, leaving nothing behind.
func TestBinaryServesAndStopsOnSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal delivery differs on Windows")
	}
	bin := buildBinary(t)
	cmd := exec.Command(bin, "--listen=127.0.0.1:0", "--seed=3")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }() // a no-op once it has exited

	line := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		if sc.Scan() {
			line <- sc.Text()
		}
	}()
	var addr string
	select {
	case l := <-line:
		const prefix = "mockmodel listening on "
		if !strings.HasPrefix(l, prefix) {
			t.Fatalf("first line = %q", l)
		}
		addr = strings.TrimPrefix(l, prefix)
	case <-time.After(15 * time.Second):
		t.Fatal("the server never reported its address")
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("the server listens on %q, want loopback", addr)
	}

	post := func() string {
		resp, err := http.Post("http://"+addr+mockmodel.ChatPath, "application/json",
			strings.NewReader(`{"model":"mock-large","messages":[{"role":"user","content":"hi there"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return string(b)
	}
	first, second := post(), post()
	if first != second || !strings.Contains(first, "chat.completion") {
		t.Fatalf("responses must be identical and valid:\n%s\n%s", first, second)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("the server must exit 0 on SIGTERM, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not exit after SIGTERM")
	}
}

func TestRunReportsAListenFailure(t *testing.T) {
	cfg := mockmodel.DefaultConfig()
	cfg.Listen = "256.256.256.256:1" // not a valid address
	if err := run(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "cannot listen") {
		t.Fatalf("run = %v, want a listen error", err)
	}
}

func TestRunStopsWhenTheContextIsCancelled(t *testing.T) {
	cfg := mockmodel.DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg) }()
	cancel() // whether cancellation arrives before or after the listener is up, run must return nil
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run = %v after cancellation, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after cancellation")
	}
}

//go:build live

package main

// Live tests exercise the real acquire() flow against an actual Testflinger
// server, using an in-process exec endpoint that forwards every command over
// ssh to an orchestrator host. They are opt-in:
//
//	go test -tags live -v -run TestLive ./...
//
// Requirements: passwordless ssh to $TF_ORCH (default talha@vpn-gateway) with
// testflinger-cli available there, and — for the adoption test — a live
// reservation on $TF_QUEUE (default megatron) matching the spec below.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

func orchHost() string {
	if h := os.Getenv("TF_ORCH"); h != "" {
		return h
	}
	return "talha@vpn-gateway"
}

// liveOrchestrator serves a real exec endpoint whose CommandFunc runs each
// command on the orchestrator over ssh — the same shape the ssh/tailscale
// wormhole chain provides in production.
func liveOrchestrator(t *testing.T) *wormhole.ExecRunner {
	t.Helper()
	run := func(ctx context.Context, cmd wormhole.Command, sink wormhole.ExecSink) error {
		argv := []string{"ssh", "-o", "BatchMode=yes", orchHost(), remoteShellCommand(cmd.Dir, cmd.Env, cmd.Argv)}
		return wormhole.RunLocalCommand(ctx, wormhole.Command{Argv: argv, Stdin: cmd.Stdin, TimeoutMs: cmd.TimeoutMs}, sink)
	}
	desc, stop, err := wormhole.ServeExecEndpoint(t.TempDir(), run)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stop() })
	orch, err := wormhole.DialExecEndpoint(desc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = orch.Close() })
	return orch
}

func liveConfig(t *testing.T, extra string) config {
	t.Helper()
	queue := os.Getenv("TF_QUEUE")
	if queue == "" {
		queue = "megatron"
	}
	raw := `{"job_queue":"` + queue + `","provision_data":{"distro":"resolute"},"ssh_keys":["gh:talhaHavadar"],"testflinger_bin":"testflinger"` + extra + `}`
	cfg, err := parseConfig([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestLiveAdopt expects acquire to find and adopt the live reservation on the
// queue instead of submitting a fresh job.
func TestLiveAdopt(t *testing.T) {
	orch := liveOrchestrator(t)
	cfg := liveConfig(t, "")

	start := time.Now()
	res, err := acquire(context.Background(), &wormhole.LinkRequest{LinkID: "live-adopt"}, cfg, orch)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Logf("acquired in %s: job=%s target=%s@%s expiry=%s adopted=%v",
		time.Since(start).Round(time.Millisecond), res.JobID, res.Target.User, res.Target.Host,
		res.Expiry.Format(time.RFC3339), res.Adopted)
	if !res.Adopted {
		t.Error("expected to adopt the live reservation, not submit a new job")
	}
	if res.Target.User == "" || res.Target.Host == "" {
		t.Errorf("incomplete target: %+v", res.Target)
	}
	if res.Expiry.Before(time.Now()) {
		t.Errorf("expiry in the past: %s", res.Expiry)
	}
	if time.Since(start) > 2*time.Minute {
		t.Errorf("adoption should be fast, took %s", time.Since(start))
	}

	// And prove the hand-off is real: run one command on the reserved machine.
	sink := &tailSink{}
	if err := runOverSSH(context.Background(), orch, cfg, res.Target, wormhole.Command{Argv: []string{"uname", "-a"}}, sink); err != nil {
		t.Fatalf("runOverSSH: %v", err)
	}
	t.Logf("uname on reserved machine (exit %d): %s", sink.exit, strings.TrimSpace(sink.stdout.String()))
	if sink.exit != 0 {
		t.Errorf("uname exited %d, stderr: %s", sink.exit, sink.stderr.String())
	}
}

// TestLiveBogusQueue expects pre-flight to fail in seconds with the server's
// own message, instead of submitting and burning the reserve timeout.
func TestLiveBogusQueue(t *testing.T) {
	orch := liveOrchestrator(t)
	cfg, err := parseConfig([]byte(`{"job_queue":"definitely-not-a-queue","ssh_keys":["gh:talhaHavadar"],"testflinger_bin":"testflinger"}`))
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err = acquire(context.Background(), &wormhole.LinkRequest{LinkID: "live-bogus"}, cfg, orch)
	if err == nil {
		t.Fatal("expected pre-flight to reject the bogus queue")
	}
	t.Logf("failed in %s with: %v", time.Since(start).Round(time.Millisecond), err)
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected the server's does-not-exist message, got: %v", err)
	}
	if time.Since(start) > 60*time.Second {
		t.Errorf("pre-flight failure should be fast, took %s", time.Since(start))
	}
}

type tailSink struct {
	stdout, stderr strings.Builder
	exit           int
}

func (s *tailSink) Stdout(p []byte)  { s.stdout.Write(p) }
func (s *tailSink) Stderr(p []byte)  { s.stderr.Write(p) }
func (s *tailSink) SetExit(code int) { s.exit = code }

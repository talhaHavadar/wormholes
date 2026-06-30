package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

// reviewHeartbeatInterval is how often the heartbeat goroutine emits a
// Progress update while the review script is running. The MCP harness drops
// tool calls that go silent on the notification channel for too long, and
// individual review steps (sbuild, fetch_orig, licensecheck) can run for
// many minutes without producing any. r.Run buffers exec output internally,
// so the underlying gRPC traffic doesn't keep the harness happy on its own.
const reviewHeartbeatInterval = 20 * time.Second

type reviewInput struct {
	Source string `json:"source" jsonschema:"local path to the source tree on the builder, OR a git URL with an optional @<branch-or-tag>; git URLs are cloned into a fresh temp workspace"`
	Depth  int    `json:"depth,omitempty" jsonschema:"git clone depth for a git source (0 = full history)"`
	PPA    string `json:"ppa,omitempty" jsonschema:"optional Launchpad PPA (owner/name); when set, prebuilt binaries are pulled and lintian-checked as the lintian_binary step"`
}

type reviewResult struct {
	OverallStatus string       `json:"overall_status"` // ok | warn | fail
	Steps         []reviewStep `json:"steps"`
	Workspace     string       `json:"workspace,omitempty"`
}

// review runs the Debian package review checklist on the source tree. Each
// step's outcome is reported individually; per-step failures never abort the
// run, so the agent always sees the full report.
func review(ctx context.Context, call *wormhole.Call, in reviewInput) (any, error) {
	if in.Source == "" {
		return nil, fmt.Errorf("source is required")
	}
	r, err := runnerFor(call, "builder")
	if err != nil {
		return nil, err
	}
	defer r.Close()

	call.Logf("info", "running review checklist on %s (can take several minutes per step)", in.Source)
	call.Progress(-1, "executing review steps")
	stop := startHeartbeat(call, reviewHeartbeatInterval)
	// The script always reads $1 as the optional ppa arg; pass empty when unset.
	res, err := r.Run(ctx, pipelineCommand(reviewBody, in.Source, in.Depth, in.PPA))
	stop()
	if err != nil {
		return nil, err
	}
	out := combine(res)
	steps := parseReviewSteps(out)
	// Script-level failure (e.g. ENABLED_STEPS drift) emits ISPKG_ERROR and
	// produces no step blocks — surface that as a hard error.
	if len(steps) == 0 {
		if e := relayError(out); e != nil {
			return nil, e
		}
		return nil, fmt.Errorf("review produced no step output (exit %d): %s", res.ExitCode, tail(out, 30))
	}
	for _, w := range warningMarkers(out) {
		call.Logf("warn", "%s", w)
	}
	for _, s := range steps {
		switch s.Status {
		case "fail":
			call.Logf("warn", "step %s failed: %s", s.Name, s.Summary)
		case "warn":
			call.Logf("info", "step %s warned: %s", s.Name, s.Summary)
		}
	}
	return reviewResult{
		OverallStatus: overallReviewStatus(steps),
		Steps:         steps,
		Workspace:     parseWorkspace(out),
	}, nil
}

// startHeartbeat emits an indeterminate Progress update every interval until
// the returned stop func runs. Safe to call stop multiple times. Concurrent
// with the caller's own use of call (the wormhole pkg serializes emits).
func startHeartbeat(call *wormhole.Call, interval time.Duration) func() {
	done := make(chan struct{})
	start := time.Now()
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				elapsed := time.Since(start).Round(time.Second)
				call.Progress(-1, fmt.Sprintf("review still running (%s elapsed)", elapsed))
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

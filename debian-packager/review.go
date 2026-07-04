package main

import (
	"context"
	"fmt"

	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

type reviewInput struct {
	Source string `json:"source" jsonschema:"local path to the source tree on the builder, OR a git URL with an optional @<branch-or-tag>; git URLs are cloned into a fresh temp workspace"`
	Depth  int    `json:"depth,omitempty" jsonschema:"git clone depth for a git source (0 = full history)"`
	PPA    string `json:"ppa,omitempty" jsonschema:"optional Launchpad PPA (owner/name); when set, prebuilt binaries are pulled and lintian-checked as the lintian_binary step"`
}

type reviewResult struct {
	OverallStatus string `json:"overall_status"` // ok | warn | fail
	// FailedSteps/WarnedSteps are a compact "name: summary" digest, emitted
	// BEFORE the steps array on purpose: MCP clients truncate large tool
	// results from the end, and a live run showed an agent losing exactly
	// the failing steps that way. Even a brutal prefix keeps the verdict.
	FailedSteps []string     `json:"failed_steps,omitempty"`
	WarnedSteps []string     `json:"warned_steps,omitempty"`
	Steps       []reviewStep `json:"steps"`
	Workspace   string       `json:"workspace,omitempty"`
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
	stop := startHeartbeat(call, "review", heartbeatInterval)
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
	// Shape the report for an LLM consumer whose harness may truncate it:
	// budget the log tails by severity and put failures first.
	trimStepLogs(steps)
	sortStepsBySeverity(steps)
	return reviewResult{
		OverallStatus: overallReviewStatus(steps),
		FailedSteps:   stepDigest(steps, "fail"),
		WarnedSteps:   stepDigest(steps, "warn"),
		Steps:         steps,
		Workspace:     parseWorkspace(out),
	}, nil
}

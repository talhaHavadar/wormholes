package main

import (
	"context"
	"fmt"

	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

type buildBinaryInput struct {
	Source       string `json:"source" jsonschema:"local path to the source tree on the builder, OR a git URL (https://, ssh://, git@host:…) with an optional @<branch-or-tag>; git URLs are cloned into a fresh temp workspace"`
	Distribution string `json:"distribution" jsonschema:"target distribution for sbuild -d (e.g. unstable, trixie, experimental)"`
	Arch         string `json:"arch,omitempty" jsonschema:"target architecture; the builder's native arch if empty"`
	Depth        int    `json:"depth,omitempty" jsonschema:"git clone depth for a git source (0 = full history; needed for gbp/pristine-tar)"`
}

type buildSourceInput struct {
	Source string `json:"source" jsonschema:"local path to the source tree on the builder, OR a git URL with an optional @<branch-or-tag>; git URLs are cloned into a fresh temp workspace"`
	Depth  int    `json:"depth,omitempty" jsonschema:"git clone depth for a git source (0 = full history; needed for gbp/pristine-tar)"`
}

type lintInput struct {
	Source string `json:"source" jsonschema:"local path to the source tree on the builder, OR a git URL with an optional @<branch-or-tag>; used to build-and-lint, or (with ppa) to read the package name/series for the artifacts to pull"`
	Depth  int    `json:"depth,omitempty" jsonschema:"git clone depth for a git source (0 = full history)"`
	PPA    string `json:"ppa,omitempty" jsonschema:"optional Launchpad PPA (owner/name); when set, prebuilt source+binary artifacts are pulled from it and linted instead of building locally (requires ubuntu-dev-tools on the builder)"`
}

type checkWatchInput struct {
	Source string `json:"source" jsonschema:"local path to the source tree (with debian/watch) on the builder, OR a git URL with an optional @<branch-or-tag>; git URLs are cloned into a fresh temp workspace"`
	Depth  int    `json:"depth,omitempty" jsonschema:"git clone depth for a git source (0 = full history)"`
}

type buildResult struct {
	Success   bool            `json:"success"`
	ExitCode  int             `json:"exit_code"`
	Changes   string          `json:"changes,omitempty"`
	Artifacts []string        `json:"artifacts"`
	Lintian   *lintianSummary `json:"lintian,omitempty"`
	Workspace string          `json:"workspace,omitempty"` // git builds: the temp clone dir (removed on success, kept on failure)
	LogTail   string          `json:"log_tail,omitempty"`
}

func buildBinaryPackage(ctx context.Context, call *wormhole.Call, in buildBinaryInput) (any, error) {
	if in.Source == "" {
		return nil, fmt.Errorf("source is required")
	}
	if in.Distribution == "" {
		return nil, fmt.Errorf("distribution is required")
	}
	r, err := runnerFor(call, "builder")
	if err != nil {
		return nil, err
	}
	defer r.Close()

	toolArgs := []string{in.Distribution}
	if in.Arch != "" {
		toolArgs = append(toolArgs, in.Arch)
	}
	call.Logf("info", "building binary package from %s", in.Source)
	call.Progress(-1, "running sbuild")
	res, err := r.Run(ctx, pipelineCommand(buildBinaryBody, in.Source, in.Depth, toolArgs...))
	if err != nil {
		return nil, err
	}
	out := combine(res)
	if err := relayError(out); err != nil {
		return nil, err
	}
	for _, w := range warningMarkers(out) {
		call.Logf("warn", "%s", w)
	}
	artifacts := findArtifacts(out)
	return buildResult{
		Success:   res.ExitCode == 0,
		ExitCode:  res.ExitCode,
		Changes:   pickExt(artifacts, ".changes"),
		Artifacts: artifacts,
		Lintian:   parseLintian(out),
		Workspace: parseWorkspace(out),
		LogTail:   tail(out, 60),
	}, nil
}

func buildSourcePackage(ctx context.Context, call *wormhole.Call, in buildSourceInput) (any, error) {
	if in.Source == "" {
		return nil, fmt.Errorf("source is required")
	}
	r, err := runnerFor(call, "builder")
	if err != nil {
		return nil, err
	}
	defer r.Close()

	call.Logf("info", "building source package from %s", in.Source)
	call.Progress(-1, "running dpkg-buildpackage -S")
	res, err := r.Run(ctx, pipelineCommand(buildSourceBody, in.Source, in.Depth))
	if err != nil {
		return nil, err
	}
	out := combine(res)
	if err := relayError(out); err != nil {
		return nil, err
	}
	for _, w := range warningMarkers(out) {
		call.Logf("warn", "%s", w)
	}
	artifacts := findArtifacts(out)
	return buildResult{
		Success:   res.ExitCode == 0,
		ExitCode:  res.ExitCode,
		Changes:   pickExt(artifacts, ".changes"),
		Artifacts: artifacts,
		Workspace: parseWorkspace(out),
		LogTail:   tail(out, 40),
	}, nil
}

type lintResult struct {
	Mode     string          `json:"mode"` // "source" (built locally) or "ppa" (pulled from Launchpad)
	ExitCode int             `json:"exit_code"`
	Summary  *lintianSummary `json:"summary,omitempty"`
	Warnings []string        `json:"warnings,omitempty"`
	LogTail  string          `json:"log_tail,omitempty"`
}

func lint(ctx context.Context, call *wormhole.Call, in lintInput) (any, error) {
	if in.Source == "" {
		return nil, fmt.Errorf("source is required")
	}
	r, err := runnerFor(call, "builder")
	if err != nil {
		return nil, err
	}
	defer r.Close()

	mode := "source"
	var toolArgs []string
	if in.PPA != "" {
		mode = "ppa"
		toolArgs = append(toolArgs, in.PPA)
		call.Logf("info", "linting %s artifacts from ppa %s", in.Source, in.PPA)
	} else {
		call.Logf("info", "building and linting source package from %s", in.Source)
	}
	call.Progress(-1, "running lintian")
	res, err := r.Run(ctx, pipelineCommand(lintBody, in.Source, in.Depth, toolArgs...))
	if err != nil {
		return nil, err
	}
	out := combine(res)
	if err := relayError(out); err != nil {
		return nil, err
	}
	warnings := warningMarkers(out)
	for _, w := range warnings {
		call.Logf("warn", "%s", w)
	}
	return lintResult{
		Mode:     mode,
		ExitCode: res.ExitCode,
		Summary:  parseLintian(out),
		Warnings: warnings,
		LogTail:  tail(out, 60),
	}, nil
}

func checkWatch(ctx context.Context, call *wormhole.Call, in checkWatchInput) (any, error) {
	if in.Source == "" {
		return nil, fmt.Errorf("source is required")
	}
	r, err := runnerFor(call, "builder")
	if err != nil {
		return nil, err
	}
	defer r.Close()

	call.Logf("info", "checking debian/watch from %s", in.Source)
	res, err := r.Run(ctx, pipelineCommand(checkWatchBody, in.Source, in.Depth))
	if err != nil {
		return nil, err
	}
	out := combine(res)
	if err := relayError(out); err != nil {
		return nil, err
	}
	w, err := parseWatch(out)
	if err != nil {
		return nil, err
	}
	return w, nil
}

// runnerFor resolves the named exec-endpoint port to a dialed runner.
func runnerFor(call *wormhole.Call, port string) (*wormhole.ExecRunner, error) {
	link, ok := call.Link(port)
	if !ok {
		return nil, fmt.Errorf("no %q builder linked; choose a %s_target", port, port)
	}
	var ep wormhole.ExecEndpointDescriptor
	if err := link.DecodeDescriptor(&ep); err != nil {
		return nil, fmt.Errorf("decoding builder endpoint: %w", err)
	}
	r, err := wormhole.DialExecEndpoint(ep)
	if err != nil {
		return nil, fmt.Errorf("dialing builder: %w", err)
	}
	return r, nil
}

package main

import (
	"context"
	"fmt"
	"path/filepath"

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
	Target string `json:"target" jsonschema:"path on the builder to a .changes, .dsc, or .deb to lint"`
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
	Workspace string          `json:"workspace,omitempty"` // git builds: the temp clone dir on the builder
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

	argv := []string{"sbuild", "-d", in.Distribution}
	if in.Arch != "" {
		argv = append(argv, "--arch", in.Arch)
	}
	argv = append(argv, "--no-clean-source")

	call.Logf("info", "building binary package from %s", in.Source)
	call.Progress(-1, "running sbuild")
	res, err := r.Run(ctx, buildCommand(in.Source, in.Depth, argv))
	if err != nil {
		return nil, err
	}
	out := combine(res)
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

	// Source-only build, unsigned (sign/dput separately). -d skips the
	// build-dependency check, which is the builder's concern, not ours.
	argv := []string{"dpkg-buildpackage", "-S", "-us", "-uc", "-d"}
	call.Logf("info", "building source package from %s", in.Source)
	call.Progress(-1, "running dpkg-buildpackage -S")
	res, err := r.Run(ctx, buildCommand(in.Source, in.Depth, argv))
	if err != nil {
		return nil, err
	}
	out := combine(res)
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
	ExitCode int             `json:"exit_code"`
	Summary  *lintianSummary `json:"summary,omitempty"`
	LogTail  string          `json:"log_tail,omitempty"`
}

func lint(ctx context.Context, call *wormhole.Call, in lintInput) (any, error) {
	if in.Target == "" {
		return nil, fmt.Errorf("target is required")
	}
	r, err := runnerFor(call, "builder")
	if err != nil {
		return nil, err
	}
	defer r.Close()

	// Run from the artifact's directory so the builder's container mount
	// includes it, and lint by basename.
	dir, name := filepath.Dir(in.Target), filepath.Base(in.Target)
	argv := []string{"lintian", "--info", "--pedantic", "--no-tag-display-limit", name}
	call.Logf("info", "linting %s (in %s)", name, dir)
	res, err := r.Run(ctx, wormhole.Command{Argv: argv, Dir: dir})
	if err != nil {
		return nil, err
	}
	out := combine(res)
	return lintResult{
		ExitCode: res.ExitCode,
		Summary:  parseLintian(out),
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

	argv := []string{"uscan", "--report", "--dehs"}
	call.Logf("info", "checking debian/watch from %s", in.Source)
	res, err := r.Run(ctx, buildCommand(in.Source, in.Depth, argv))
	if err != nil {
		return nil, err
	}
	out := combine(res)
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

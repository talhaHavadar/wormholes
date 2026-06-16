package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

const (
	defaultImage         = "ghcr.io/talhahavadar/contained-debdev:ubuntu-devel"
	defaultContainedPath = "/usr/local/bin/contained"
	defaultRuntime       = "podman"
)

// config is the admin-supplied link configuration.
type config struct {
	// Image is the contained-debdev container image to run.
	Image string `json:"image"`
	// ContainedPath is the absolute path to the `contained` script on the
	// target. It is always invoked by absolute path, so the target's PATH is
	// irrelevant.
	ContainedPath string `json:"contained_path"`
	// Runtime is the container runtime contained should use: "podman"
	// (default, rootless) or "docker".
	Runtime string `json:"runtime"`
	// EnsureDeps bootstraps the contained script and runtime on the target when
	// missing (idempotent). Off by default; intended for clean machines.
	EnsureDeps bool `json:"ensure_deps"`
	// InstallRuntime lets ensure_deps apt-get install the runtime when absent.
	// Off by default.
	InstallRuntime bool `json:"install_runtime"`
}

func parseConfig(raw json.RawMessage) (config, error) {
	c := config{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &c); err != nil {
			return config{}, fmt.Errorf("contained-debdev config: %w", err)
		}
	}
	if c.Image == "" {
		c.Image = defaultImage
	}
	if c.ContainedPath == "" {
		c.ContainedPath = defaultContainedPath
	}
	if c.Runtime == "" {
		c.Runtime = defaultRuntime
	}
	return c, nil
}

// openLink brings the exec-endpoint up: it decides where the container runs
// (local, or through a linked upstream host), optionally bootstraps deps, and
// serves an exec service that wraps every command with `contained`.
func openLink(ctx context.Context, req *wormhole.LinkRequest) (*wormhole.ActiveLink, error) {
	cfg, err := parseConfig(req.Config)
	if err != nil {
		return nil, err
	}

	// Where do commands run? Through an upstream exec-endpoint if one is linked
	// (ssh, testflinger, ...), otherwise on the local host.
	base := wormhole.RunLocalCommand
	var upstream *wormhole.ExecRunner
	if link, ok := findLink(req.Links, wormhole.PortTypeExecEndpoint); ok {
		var ep wormhole.ExecEndpointDescriptor
		if err := link.DecodeDescriptor(&ep); err != nil {
			return nil, fmt.Errorf("decoding host exec-endpoint: %w", err)
		}
		upstream, err = wormhole.DialExecEndpoint(ep)
		if err != nil {
			return nil, fmt.Errorf("dialing host exec-endpoint: %w", err)
		}
		base = runnerCommandFunc(upstream)
	}

	cleanup := func() {
		if upstream != nil {
			_ = upstream.Close()
		}
	}

	if err := preflight(ctx, req, cfg, base); err != nil {
		cleanup()
		return nil, err
	}

	run := func(ctx context.Context, cmd wormhole.Command, sink wormhole.ExecSink) error {
		return base(ctx, wrap(cfg, cmd), sink)
	}
	desc, stop, err := wormhole.ServeExecEndpoint(wormhole.LinkSocketDir(req.LinkID), run)
	if err != nil {
		cleanup()
		return nil, err
	}
	return &wormhole.ActiveLink{
		Descriptor: desc,
		Close: func() error {
			_ = stop()
			cleanup()
			return nil
		},
	}, nil
}

// wrap turns a build command into `contained -c <image> -- <argv...>`, run in
// the same working directory so contained's `$PWD/..:/work` mount lines up.
func wrap(cfg config, cmd wormhole.Command) wormhole.Command {
	argv := []string{cfg.ContainedPath, "-c", cfg.Image, "--"}
	argv = append(argv, cmd.Argv...)

	env := map[string]string{"CONTAINED_CONTAINER_RUNTIME": cfg.Runtime}
	for k, v := range cmd.Env {
		env[k] = v
	}
	return wormhole.Command{
		Argv:      argv,
		Env:       env,
		Dir:       cmd.Dir,
		Stdin:     cmd.Stdin,
		TimeoutMs: cmd.TimeoutMs,
	}
}

// runnerCommandFunc adapts an upstream ExecRunner to a CommandFunc. The SDK's
// runner is request/response rather than streaming, so a long remote command
// reports its collected output when it finishes.
func runnerCommandFunc(r *wormhole.ExecRunner) wormhole.CommandFunc {
	return func(ctx context.Context, cmd wormhole.Command, sink wormhole.ExecSink) error {
		res, err := r.Run(ctx, cmd)
		if res != nil {
			if len(res.Stdout) > 0 {
				sink.Stdout(res.Stdout)
			}
			if len(res.Stderr) > 0 {
				sink.Stderr(res.Stderr)
			}
			sink.SetExit(res.ExitCode)
		}
		return err
	}
}

func findLink(links []wormhole.Link, portType string) (wormhole.Link, bool) {
	for _, l := range links {
		if l.Type == portType {
			return l, true
		}
	}
	return wormhole.Link{}, false
}

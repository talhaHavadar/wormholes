package main

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

// containedScript is a copy of the `contained` script, baked into the binary so
// a clean target can be bootstrapped without fetching anything.
//
//go:embed contained.sh
var containedScript string

// ensureDeps makes the `contained` script and a container runtime available on
// the target (wherever base runs). It is idempotent and safe to repeat, and
// streams progress via the link request so a slow apt/install isn't silent.
func ensureDeps(ctx context.Context, req *wormhole.LinkRequest, cfg config, base wormhole.CommandFunc) error {
	req.Progress(-1, "ensuring build dependencies on target")

	// 1. The contained script, installed to an absolute path (invoked by that
	//    path, so the target's PATH never matters). Try an unprivileged move
	//    first, then sudo.
	if code, _, _ := runCapture(ctx, base, wormhole.Command{
		Argv: []string{"sh", "-c", "test -x " + shellQuote(cfg.ContainedPath)},
	}); code != 0 {
		req.Logf("info", "installing contained to %s", cfg.ContainedPath)
		dest := shellQuote(cfg.ContainedPath)
		script := fmt.Sprintf(
			`set -e; tmp=$(mktemp); cat >"$tmp"; chmod 0755 "$tmp"; `+
				`(mkdir -p "$(dirname %s)" 2>/dev/null || sudo mkdir -p "$(dirname %s)"); `+
				`(mv "$tmp" %s 2>/dev/null || sudo mv "$tmp" %s)`,
			dest, dest, dest, dest)
		if code, _, stderr := runCapture(ctx, base, wormhole.Command{
			Argv:  []string{"sh", "-c", script},
			Stdin: []byte(containedScript),
		}); code != 0 {
			return fmt.Errorf("installing contained to %s: %s", cfg.ContainedPath, strings.TrimSpace(stderr))
		}
	}

	// 2. A container runtime. Prefer the configured one (podman, rootless).
	if code, _, _ := runCapture(ctx, base, wormhole.Command{
		Argv: []string{"sh", "-c", "command -v " + shellQuote(cfg.Runtime)},
	}); code != 0 {
		if !cfg.InstallRuntime {
			return fmt.Errorf("container runtime %q not found on target (set install_runtime to install it)", cfg.Runtime)
		}
		req.Logf("info", "installing container runtime %s", cfg.Runtime)
		install := "sudo apt-get update && sudo apt-get install -y " + shellQuote(cfg.Runtime)
		if code, _, stderr := runCapture(ctx, base, wormhole.Command{
			Argv: []string{"sh", "-c", install},
		}); code != 0 {
			return fmt.Errorf("installing %s: %s", cfg.Runtime, strings.TrimSpace(stderr))
		}
	}

	req.Progress(-1, "build dependencies ready")
	return nil
}

// captureSink collects a command's output for inspection.
type captureSink struct {
	stdout strings.Builder
	stderr strings.Builder
	exit   int
}

func (c *captureSink) Stdout(p []byte)  { c.stdout.Write(p) }
func (c *captureSink) Stderr(p []byte)  { c.stderr.Write(p) }
func (c *captureSink) SetExit(code int) { c.exit = code }

// runCapture runs cmd through base and returns its exit code, stdout, stderr.
func runCapture(ctx context.Context, base wormhole.CommandFunc, cmd wormhole.Command) (int, string, string) {
	s := &captureSink{exit: -1}
	if err := base(ctx, cmd, s); err != nil {
		return -1, s.stdout.String(), s.stderr.String() + err.Error()
	}
	return s.exit, s.stdout.String(), s.stderr.String()
}

// shellQuote single-quotes s for safe inclusion in an `sh -c` string.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

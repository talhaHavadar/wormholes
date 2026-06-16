package main

import (
	"context"
	"testing"

	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

func TestParseConfigDefaults(t *testing.T) {
	c, err := parseConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Image != defaultImage || c.ContainedPath != defaultContainedPath || c.Runtime != defaultRuntime {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.EnsureDeps || c.InstallRuntime {
		t.Fatalf("bootstrap should be off by default: %+v", c)
	}
}

func TestParseConfigOverrides(t *testing.T) {
	c, err := parseConfig([]byte(`{"image":"img:trixie","contained_path":"/opt/contained","runtime":"docker","ensure_deps":true,"install_runtime":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Image != "img:trixie" || c.ContainedPath != "/opt/contained" || c.Runtime != "docker" {
		t.Fatalf("overrides not applied: %+v", c)
	}
	if !c.EnsureDeps || !c.InstallRuntime {
		t.Fatalf("bootstrap flags not applied: %+v", c)
	}
}

func TestWrap(t *testing.T) {
	cfg := config{Image: "img:trixie", ContainedPath: "/usr/local/bin/contained", Runtime: "podman"}
	out := wrap(cfg, wormhole.Command{
		Argv: []string{"sbuild", "-d", "trixie"},
		Dir:  "/work/pkg",
		Env:  map[string]string{"DEBEMAIL": "me@example.com"},
	})

	want := []string{"/usr/local/bin/contained", "-c", "img:trixie", "--", "sbuild", "-d", "trixie"}
	if len(out.Argv) != len(want) {
		t.Fatalf("argv = %v, want %v", out.Argv, want)
	}
	for i := range want {
		if out.Argv[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, out.Argv[i], want[i])
		}
	}
	if out.Dir != "/work/pkg" {
		t.Fatalf("dir = %q, want /work/pkg", out.Dir)
	}
	if out.Env["CONTAINED_CONTAINER_RUNTIME"] != "podman" {
		t.Fatalf("runtime env = %q, want podman", out.Env["CONTAINED_CONTAINER_RUNTIME"])
	}
	if out.Env["DEBEMAIL"] != "me@example.com" {
		t.Fatalf("passthrough env lost: %v", out.Env)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/usr/local/bin/contained"); got != "'/usr/local/bin/contained'" {
		t.Fatalf("shellQuote = %q", got)
	}
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Fatalf("shellQuote with quote = %q", got)
	}
}

// TestEnsureDepsNoopWhenPresent verifies the bootstrap is a no-op (no install
// attempts) when the contained script and runtime already exist on the target.
func TestEnsureDepsNoopWhenPresent(t *testing.T) {
	var installs int
	base := func(ctx context.Context, cmd wormhole.Command, sink wormhole.ExecSink) error {
		joined := ""
		if len(cmd.Argv) >= 3 {
			joined = cmd.Argv[2] // the `sh -c <script>` body
		}
		switch {
		case contains(joined, "test -x"), contains(joined, "command -v"):
			sink.SetExit(0) // already present
		default:
			installs++
			sink.SetExit(0)
		}
		return nil
	}
	req := &wormhole.LinkRequest{}
	cfg := config{ContainedPath: defaultContainedPath, Runtime: defaultRuntime}
	if err := ensureDeps(context.Background(), req, cfg, base); err != nil {
		t.Fatalf("ensureDeps: %v", err)
	}
	if installs != 0 {
		t.Fatalf("expected no install commands when deps present, got %d", installs)
	}
}

func TestPreflightMissingContained(t *testing.T) {
	base := func(ctx context.Context, cmd wormhole.Command, sink wormhole.ExecSink) error {
		body := ""
		if len(cmd.Argv) >= 3 {
			body = cmd.Argv[2]
		}
		if contains(body, "test -x") {
			sink.SetExit(1) // contained missing
		} else {
			sink.SetExit(0)
		}
		return nil
	}
	err := preflight(context.Background(), &wormhole.LinkRequest{},
		config{ContainedPath: "/usr/local/bin/contained", Runtime: "docker"}, base)
	if err == nil || !contains(err.Error(), "contained not found") {
		t.Fatalf("want a clear contained-not-found error, got %v", err)
	}
}

func TestPreflightOK(t *testing.T) {
	base := func(ctx context.Context, cmd wormhole.Command, sink wormhole.ExecSink) error {
		sink.SetExit(0)
		return nil
	}
	if err := preflight(context.Background(), &wormhole.LinkRequest{},
		config{ContainedPath: "/usr/local/bin/contained", Runtime: "docker"}, base); err != nil {
		t.Fatalf("preflight should pass when deps present: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

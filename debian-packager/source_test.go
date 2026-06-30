package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestIsGitSource(t *testing.T) {
	git := []string{
		"https://github.com/x/repo.git",
		"http://host/x.git",
		"git://host/x.git",
		"ssh://git@host/x.git",
		"git+ssh://git@host/x.git",
		"git@github.com:x/repo.git",
		"git@github.com:x/repo.git@main",
	}
	for _, s := range git {
		if !isGitSource(s) {
			t.Errorf("isGitSource(%q) = false, want true", s)
		}
	}
	local := []string{
		"/home/me/pkg", "./pkg", "../build/pkg", "relative/path",
		"/Users/talha/projects/debian/rocm-core",
	}
	for _, s := range local {
		if isGitSource(s) {
			t.Errorf("isGitSource(%q) = true, want false", s)
		}
	}
}

func TestSplitGitRef(t *testing.T) {
	cases := []struct{ in, repo, ref string }{
		{"https://github.com/x/repo.git@main", "https://github.com/x/repo.git", "main"},
		{"https://github.com/x/repo.git", "https://github.com/x/repo.git", ""},
		{"git@github.com:x/repo.git@dev", "git@github.com:x/repo.git", "dev"},
		{"git@github.com:x/repo.git", "git@github.com:x/repo.git", ""},
		{"ssh://git@host/p/repo.git", "ssh://git@host/p/repo.git", ""},
		{"ssh://git@host/p/repo.git@v1.2", "ssh://git@host/p/repo.git", "v1.2"},
		{"https://user:tok@host/x/repo.git@feature/foo", "https://user:tok@host/x/repo.git", "feature/foo"},
		{"git@github.com:x/repo.git@feature/foo", "git@github.com:x/repo.git", "feature/foo"},
	}
	for _, c := range cases {
		repo, ref := splitGitRef(c.in)
		if repo != c.repo || ref != c.ref {
			t.Errorf("splitGitRef(%q) = (%q, %q), want (%q, %q)", c.in, repo, ref, c.repo, c.ref)
		}
	}
}

func TestPipelineCommandLocal(t *testing.T) {
	cmd := pipelineCommand(buildBinaryBody, "/home/me/pkg", 0, "unstable")
	if cmd.Dir != "/home/me/pkg" {
		t.Fatalf("local source should set Dir, got %q", cmd.Dir)
	}
	// sh -c <script> debian-packager <kind> <repo> <ref> <depth> <toolargs...>
	want := []string{"sh", "-c", preludeScript + "\n" + buildBinaryBody, "debian-packager", "local", "/home/me/pkg", "", "0", "unstable"}
	if len(cmd.Argv) != len(want) {
		t.Fatalf("argv = %v", cmd.Argv)
	}
	for i := range want {
		if cmd.Argv[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, cmd.Argv[i], want[i])
		}
	}
}

func TestPipelineCommandGit(t *testing.T) {
	cmd := pipelineCommand(buildBinaryBody, "https://github.com/x/repo.git@main", 1, "trixie", "amd64")
	if cmd.Dir != "" {
		t.Fatalf("git source should not set Dir, got %q", cmd.Dir)
	}
	if cmd.Argv[2] != preludeScript+"\n"+buildBinaryBody {
		t.Fatalf("script is not prelude+body")
	}
	rest := cmd.Argv[3:]
	want := []string{"debian-packager", "git", "https://github.com/x/repo.git", "main", "1", "trixie", "amd64"}
	if len(rest) != len(want) {
		t.Fatalf("args = %v", rest)
	}
	for i := range want {
		if rest[i] != want[i] {
			t.Fatalf("arg[%d] = %q, want %q", i, rest[i], want[i])
		}
	}
}

func TestPipelineCommandGitNoRefNoDepth(t *testing.T) {
	cmd := pipelineCommand(checkWatchBody, "git@github.com:x/repo.git", 0)
	rest := cmd.Argv[3:] // debian-packager git <repo> <ref> <depth>
	if rest[1] != "git" || rest[2] != "git@github.com:x/repo.git" {
		t.Fatalf("kind/repo wrong: %v", rest)
	}
	if rest[3] != "" {
		t.Errorf("no ref should pass empty ref, got %q", rest[3])
	}
	if rest[4] != "0" {
		t.Errorf("depth should be 0, got %q", rest[4])
	}
}

// TestScriptsSyntax checks every embedded script (prelude + each body) parses
// under /bin/sh, so a broken script fails the build rather than a tool call.
func TestScriptsSyntax(t *testing.T) {
	bodies := map[string]string{
		"build-source": buildSourceBody,
		"build-binary": buildBinaryBody,
		"check-watch":  checkWatchBody,
		"lint":         lintBody,
		"review":       reviewBody,
	}
	for name, body := range bodies {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(preludeScript + "\n" + body)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("sh -n failed for %s: %v\n%s", name, err, out)
		}
	}
}

func TestParseWorkspace(t *testing.T) {
	out := "log line\nISPKG_WORKSPACE=/work/wh/interstellar-build-abc123\nmore log\n"
	if got := parseWorkspace(out); got != "/work/wh/interstellar-build-abc123" {
		t.Fatalf("parseWorkspace = %q", got)
	}
	if got := parseWorkspace("no workspace line here"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

package main

import (
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

func TestBuildCommandLocal(t *testing.T) {
	cmd := buildCommand("/home/me/pkg", 0, []string{"sbuild", "-d", "unstable"})
	if cmd.Dir != "/home/me/pkg" {
		t.Fatalf("dir = %q, want /home/me/pkg", cmd.Dir)
	}
	if len(cmd.Argv) != 3 || cmd.Argv[0] != "sbuild" {
		t.Fatalf("argv = %v", cmd.Argv)
	}
}

func TestBuildCommandGit(t *testing.T) {
	cmd := buildCommand("https://github.com/x/repo.git@main", 1, []string{"sbuild", "-d", "trixie"})
	if cmd.Dir != "" {
		t.Fatalf("git build should not set Dir, got %q", cmd.Dir)
	}
	if len(cmd.Argv) != 3 || cmd.Argv[0] != "sh" || cmd.Argv[1] != "-c" {
		t.Fatalf("argv = %v", cmd.Argv)
	}
	s := cmd.Argv[2]
	for _, want := range []string{
		"rm -rf -- interstellar-build-*",
		"mktemp -d interstellar-build-XXXXXX",
		"ISPKG_WORKSPACE=$d",
		"'git' 'clone'",
		"'--branch' 'main'",
		"'--depth' '1'",
		"'https://github.com/x/repo.git'",
		`"$d/pkg"`,
		`mkdir -p "$d/build-area"`,
		`cd "$d/pkg"`,
		"exec 'sbuild' '-d' 'trixie'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q\n--- script ---\n%s", want, s)
		}
	}
}

func TestBuildCommandGitNoRefNoDepth(t *testing.T) {
	cmd := buildCommand("git@github.com:x/repo.git", 0, []string{"uscan", "--report"})
	s := cmd.Argv[2]
	if strings.Contains(s, "--branch") {
		t.Errorf("no ref should omit --branch:\n%s", s)
	}
	if strings.Contains(s, "--depth") {
		t.Errorf("depth 0 should omit --depth:\n%s", s)
	}
	if !strings.Contains(s, "'git@github.com:x/repo.git'") {
		t.Errorf("repo missing:\n%s", s)
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

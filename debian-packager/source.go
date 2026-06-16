package main

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

// gitWorkspacePrefix names the per-build temp workspaces created on the builder
// for git sources. Previous workspaces with this prefix are removed before each
// build so every run starts clean.
const gitWorkspacePrefix = "interstellar-build-"

var gitSchemes = []string{
	"https://", "http://", "git://", "ssh://",
	"git+ssh://", "git+https://", "git+http://",
}

// isGitSource reports whether source is a git URL rather than a local path.
func isGitSource(s string) bool {
	for _, p := range gitSchemes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	// scp-like syntax: user@host:path (no scheme, '@' before a ':').
	if !strings.Contains(s, "://") {
		if at := strings.IndexByte(s, '@'); at > 0 {
			if strings.IndexByte(s[at+1:], ':') >= 0 {
				return true
			}
		}
	}
	return false
}

// splitGitRef splits an optional "@<ref>" (branch or tag) off a git URL. The
// ref is parsed only in the path region, so "git@host"/"user@host" userinfo is
// never mistaken for a ref. No "@ref" means git clones the default branch.
func splitGitRef(src string) (repo, ref string) {
	pathStart := 0
	if i := strings.Index(src, "://"); i >= 0 {
		rest := src[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			pathStart = i + 3 + j
		} else {
			pathStart = len(src)
		}
	} else if i := strings.IndexByte(src, ':'); i >= 0 {
		pathStart = i + 1 // scp-like user@host:path
	}
	if k := strings.LastIndexByte(src[pathStart:], '@'); k >= 0 {
		idx := pathStart + k
		return src[:idx], src[idx+1:]
	}
	return src, ""
}

// buildCommand returns the Command that runs buildArgv against source. A local
// path runs in place (Dir = path). A git URL is cloned into a fresh temp
// workspace on the builder — previous "interstellar-build-*" workspaces are
// removed first — then buildArgv runs inside the clone. Everything stays under
// the builder's working directory so it survives the container mount, and the
// chosen workspace is echoed as "ISPKG_WORKSPACE=<path>".
func buildCommand(source string, depth int, buildArgv []string) wormhole.Command {
	if !isGitSource(source) {
		return wormhole.Command{Argv: buildArgv, Dir: source}
	}
	repo, ref := splitGitRef(source)

	clone := []string{"git", "clone"}
	if ref != "" {
		clone = append(clone, "--branch", ref)
	}
	if depth > 0 {
		clone = append(clone, "--depth", strconv.Itoa(depth))
	}
	clone = append(clone, "--", repo)

	script := strings.Join([]string{
		"set -eu",
		"rm -rf -- " + gitWorkspacePrefix + "* 2>/dev/null || true",
		"tmp=$(mktemp -d " + gitWorkspacePrefix + "XXXXXX)",
		`d=$(cd "$tmp" && pwd)`,
		`echo "ISPKG_WORKSPACE=$d"`,
		shJoin(clone) + ` "$d/pkg"`,
		// sbuild's build_dir is ../build-area; a fresh clone has no sibling, so
		// create it. Harmless for source builds / uscan.
		`mkdir -p "$d/build-area"`,
		`cd "$d/pkg"`,
		"exec " + shJoin(buildArgv),
	}, "\n")
	return wormhole.Command{Argv: []string{"sh", "-c", script}}
}

func shJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var workspaceRE = regexp.MustCompile(`(?m)^ISPKG_WORKSPACE=(.+)$`)

// parseWorkspace returns the git-build workspace path echoed by buildCommand.
func parseWorkspace(out string) string {
	if m := workspaceRE.FindStringSubmatch(out); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

package main

import (
	"embed"
	"regexp"
	"strconv"
	"strings"

	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

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

// The embedded shell stage library and per-tool bodies. The prelude is
// prepended to each body (see pipelineCommand) so a tool composes only the
// stages it needs, self-contained in one builder invocation.
var (
	//go:embed scripts/prelude.sh
	preludeScript string
	//go:embed scripts/build-source.sh
	buildSourceBody string
	//go:embed scripts/build-binary.sh
	buildBinaryBody string
	//go:embed scripts/check-watch.sh
	checkWatchBody string
	//go:embed scripts/lint.sh
	lintBody string

	// The review body is split for maintainability: framework (args, config,
	// step infrastructure), one file per step, and the runner. Assembled
	// below in that order — steps are pure function definitions, so their
	// relative order (ReadDir's, sorted by filename) doesn't matter as long
	// as they all precede the runner.
	//go:embed scripts/review/framework.sh
	reviewFrameworkScript string
	//go:embed scripts/review/runner.sh
	reviewRunnerScript string
	//go:embed scripts/review/steps/*.sh
	reviewStepFiles embed.FS

	reviewBody = assembleReviewBody()
)

// assembleReviewBody concatenates framework + every steps/*.sh + runner into
// the single review tool body. Panics on embed inconsistencies, which can
// only happen at build time (the files are compiled in).
func assembleReviewBody() string {
	entries, err := reviewStepFiles.ReadDir("scripts/review/steps")
	if err != nil {
		panic(err)
	}
	var b strings.Builder
	b.WriteString(reviewFrameworkScript)
	for _, e := range entries {
		data, err := reviewStepFiles.ReadFile("scripts/review/steps/" + e.Name())
		if err != nil {
			panic(err)
		}
		b.WriteString("\n")
		b.Write(data)
	}
	b.WriteString("\n")
	b.WriteString(reviewRunnerScript)
	return b.String()
}

// pipelineCommand builds a self-contained command: the shared prelude plus the
// given tool body, invoked with the source-prep args (kind/repo/ref/depth)
// followed by tool-specific args. A local source sets Dir so the runner (and
// contained's mount) sees the tree in place; a git source is cloned in-script,
// so it carries no Dir.
func pipelineCommand(body, source string, depth int, toolArgs ...string) wormhole.Command {
	kind, repo, ref := "local", source, ""
	var dir string
	if isGitSource(source) {
		kind = "git"
		repo, ref = splitGitRef(source)
	} else {
		dir = source
	}
	argv := []string{"sh", "-c", preludeScript + "\n" + body, "debian-packager", kind, repo, ref, strconv.Itoa(depth)}
	argv = append(argv, toolArgs...)
	return wormhole.Command{Argv: argv, Dir: dir}
}

var workspaceRE = regexp.MustCompile(`(?m)^ISPKG_WORKSPACE=(.+)$`)

// parseWorkspace returns the git-build workspace path echoed by acquire_source.
func parseWorkspace(out string) string {
	if m := workspaceRE.FindStringSubmatch(out); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

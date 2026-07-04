package main

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

// combine joins stdout and stderr for parsing; both can carry build output.
func combine(res *wormhole.ExecResult) string {
	if res == nil {
		return ""
	}
	return string(res.Stdout) + "\n" + string(res.Stderr)
}

var (
	errorMarkerRE   = regexp.MustCompile(`(?m)^ISPKG_ERROR: (.+)$`)
	warningMarkerRE = regexp.MustCompile(`(?m)^ISPKG_WARNING: (.+)$`)
)

// relayError turns any ISPKG_ERROR markers the scripts emit into an error to
// surface to the user — prerequisite failures (orig-tarball fetch, a missing
// helper, nothing to lint) rather than a tool's own non-zero exit. Returns nil
// when there are none.
func relayError(out string) error {
	m := errorMarkerRE.FindAllStringSubmatch(out, -1)
	if len(m) == 0 {
		return nil
	}
	msgs := make([]string, len(m))
	for i, g := range m {
		msgs[i] = strings.TrimSpace(g[1])
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}

// warningMarkers returns the ISPKG_WARNING messages the scripts emit (e.g. a
// kept-for-debugging workspace, or "source-only lint").
func warningMarkers(out string) []string {
	m := warningMarkerRE.FindAllStringSubmatch(out, -1)
	if len(m) == 0 {
		return nil
	}
	msgs := make([]string, len(m))
	for i, g := range m {
		msgs[i] = strings.TrimSpace(g[1])
	}
	return msgs
}

// artifactRE matches Debian artifact paths/filenames in build output. The
// character class keeps `/` so relative paths (e.g. ../build-area/foo.deb)
// survive, but stops at quotes and whitespace.
var artifactRE = regexp.MustCompile(`[A-Za-z0-9._~+/-]+\.(?:changes|buildinfo|dsc|deb|ddeb|udeb)\b`)

// findArtifacts collects unique Debian artifact references from build output.
func findArtifacts(out string) []string {
	seen := map[string]bool{}
	var res []string
	for _, m := range artifactRE.FindAllString(out, -1) {
		if !seen[m] {
			seen[m] = true
			res = append(res, m)
		}
	}
	sort.Strings(res)
	return res
}

func pickExt(artifacts []string, ext string) string {
	for _, a := range artifacts {
		if strings.HasSuffix(a, ext) {
			return a
		}
	}
	return ""
}

type lintianSummary struct {
	Errors       int      `json:"errors"`
	Warnings     int      `json:"warnings"`
	Info         int      `json:"info"`
	Pedantic     int      `json:"pedantic"`
	Experimental int      `json:"experimental,omitempty"`
	Tags         []string `json:"tags,omitempty"`
}

// lintianLineRE matches a lintian tag line, e.g. "W: pkg: tag-name extra".
var lintianLineRE = regexp.MustCompile(`(?m)^([EWIPX]):\s+\S+:\s+(\S+)`)

// parseLintian summarizes lintian tag lines from output. Returns nil when none
// are present (e.g. a clean package or non-lintian output).
func parseLintian(out string) *lintianSummary {
	matches := lintianLineRE.FindAllStringSubmatch(out, -1)
	if len(matches) == 0 {
		return nil
	}
	s := &lintianSummary{}
	for _, m := range matches {
		switch m[1] {
		case "E":
			s.Errors++
		case "W":
			s.Warnings++
		case "I":
			s.Info++
		case "P":
			s.Pedantic++
		case "X":
			s.Experimental++
		}
		s.Tags = append(s.Tags, m[1]+": "+m[2])
	}
	return s
}

type watchResult struct {
	Package         string `json:"package,omitempty"`
	CurrentVersion  string `json:"current_version,omitempty"`
	UpstreamVersion string `json:"upstream_version,omitempty"`
	NewerAvailable  bool   `json:"newer_available"`
	Status          string `json:"status,omitempty"`
	UpstreamURL     string `json:"upstream_url,omitempty"`
	Warnings        string `json:"warnings,omitempty"`
	Errors          string `json:"errors,omitempty"`
}

type dehs struct {
	Package         string `xml:"package"`
	DebianUversion  string `xml:"debian-uversion"`
	UpstreamVersion string `xml:"upstream-version"`
	UpstreamURL     string `xml:"upstream-url"`
	Status          string `xml:"status"`
	Warnings        string `xml:"warnings"`
	Errors          string `xml:"errors"`
}

// parseWatch extracts the DEHS report uscan --dehs prints (it may be embedded
// among other log lines).
func parseWatch(out string) (watchResult, error) {
	start := strings.Index(out, "<dehs>")
	end := strings.Index(out, "</dehs>")
	if start < 0 || end < 0 {
		return watchResult{}, fmt.Errorf("no DEHS output from uscan: %s", strings.TrimSpace(tail(out, 20)))
	}
	var d dehs
	if err := xml.Unmarshal([]byte(out[start:end+len("</dehs>")]), &d); err != nil {
		return watchResult{}, fmt.Errorf("parsing uscan DEHS: %w", err)
	}
	return watchResult{
		Package:         d.Package,
		CurrentVersion:  d.DebianUversion,
		UpstreamVersion: d.UpstreamVersion,
		UpstreamURL:     d.UpstreamURL,
		Status:          strings.TrimSpace(d.Status),
		Warnings:        strings.TrimSpace(d.Warnings),
		Errors:          strings.TrimSpace(d.Errors),
		NewerAvailable:  d.UpstreamVersion != "" && d.UpstreamVersion != d.DebianUversion,
	}, nil
}

type reviewStep struct {
	Name      string `json:"name"`
	Status    string `json:"status"` // ok | warn | fail | skipped
	Exit      int    `json:"exit_code"`
	Summary   string `json:"summary,omitempty"`
	AgentHint string `json:"agent_hint,omitempty"`
	LogTail   string `json:"log_tail,omitempty"`
}

var (
	stepBeginRE   = regexp.MustCompile(`^ISPKG_STEP_BEGIN:\s+(\S+)\s*$`)
	stepEndRE     = regexp.MustCompile(`^ISPKG_STEP_END:\s+(\S+)\s+exit=(-?\d+)\s*$`)
	stepStatusRE  = regexp.MustCompile(`^ISPKG_STEP_STATUS:\s+(\S+)\s*$`)
	stepSummaryRE = regexp.MustCompile(`^ISPKG_STEP_SUMMARY:\s+(.*)$`)
	stepHintRE    = regexp.MustCompile(`^ISPKG_STEP_HINT:\s+(.*)$`)
)

// Caps on per-step log capture, sized so a worst-case 20-step report fits
// inside gRPC's default 4 MiB message limit with headroom. Hit when a step
// dumps a lot (copyright_licensecheck on a big tree) or one long line (a
// binary blob, a base64 chunk, a single grep match against a giant file).
const (
	// maxStepLineBytes truncates any single line over this length, with a
	// trailing "[line truncated, N bytes]" marker. Stops one runaway line
	// from consuming the whole step budget.
	maxStepLineBytes = 4 * 1024
	// maxStepLogBytes is the upper bound on a step's LogTail. The TAIL is
	// kept (errors are usually at the bottom); dropped earlier lines are
	// announced with a "[N earlier line(s) truncated]" header.
	maxStepLogBytes = 64 * 1024
)

// parseReviewSteps walks the combined output and produces one reviewStep per
// ISPKG_STEP_BEGIN/END pair in the order they appear. Lines bearing step
// markers are stripped from the captured log; the rest is capped to
// maxStepLogBytes (tail-preserving) with per-line truncation.
//
// Robustness: a BEGIN with no matching END (script crashed mid-step) flushes
// the partial step as fail with exit=-1; an END with no BEGIN is dropped.
func parseReviewSteps(out string) []reviewStep {
	var steps []reviewStep
	var current *reviewStep
	var logLines []string

	flush := func(exit int) {
		if current == nil {
			return
		}
		current.Exit = exit
		if current.Status == "" {
			if exit == 0 {
				current.Status = "ok"
			} else {
				current.Status = "fail"
			}
		}
		if len(logLines) > 0 {
			current.LogTail = tailBytes(logLines, maxStepLogBytes)
		}
		// A failing step must never report an empty summary — the digest and
		// the gateway logs both lean on it (a live run surfaced `step watch
		// failed: ` with nothing after the colon).
		if current.Status == "fail" && current.Summary == "" {
			if exit == -1 {
				current.Summary = "step did not complete (interrupted before its END marker) — see log_tail"
			} else {
				current.Summary = fmt.Sprintf("exited %d without a summary — see log_tail", exit)
			}
		}
		steps = append(steps, *current)
		current = nil
		logLines = nil
	}

	for _, line := range strings.Split(out, "\n") {
		if m := stepBeginRE.FindStringSubmatch(line); m != nil {
			if current != nil {
				flush(-1) // a partial step interrupted by the next BEGIN
			}
			current = &reviewStep{Name: m[1]}
			continue
		}
		if m := stepEndRE.FindStringSubmatch(line); m != nil {
			if current == nil {
				continue
			}
			exit, _ := strconv.Atoi(m[2])
			flush(exit)
			continue
		}
		if current == nil {
			continue
		}
		if m := stepStatusRE.FindStringSubmatch(line); m != nil {
			current.Status = m[1]
			continue
		}
		if m := stepSummaryRE.FindStringSubmatch(line); m != nil {
			current.Summary = strings.TrimSpace(m[1])
			continue
		}
		if m := stepHintRE.FindStringSubmatch(line); m != nil {
			current.AgentHint = strings.TrimSpace(m[1])
			continue
		}
		logLines = append(logLines, truncateLine(line, maxStepLineBytes))
	}
	if current != nil {
		flush(-1)
	}
	return steps
}

// truncateLine clips line to at most maxBytes and appends a marker noting how
// much was dropped. Stops runaway single-line dumps (binary blobs in license
// reports, grep matches inside a giant minified file) from consuming the
// whole per-step log budget.
func truncateLine(line string, maxBytes int) string {
	if len(line) <= maxBytes {
		return line
	}
	return line[:maxBytes] + fmt.Sprintf("… [line truncated, %d bytes dropped]", len(line)-maxBytes)
}

// tailBytes joins the tail of lines that fits within maxBytes (including the
// joining newlines), prefixing the result with "[N earlier line(s) truncated]"
// when anything was dropped. If even the final line alone exceeds maxBytes
// the line itself is byte-truncated from the front (we want the END of a
// runaway final line — that's usually where the error sits).
func tailBytes(lines []string, maxBytes int) string {
	if len(lines) == 0 {
		return ""
	}
	total := 0
	keep := 0
	for i := len(lines) - 1; i >= 0; i-- {
		add := len(lines[i])
		if keep > 0 {
			add++ // newline joining this line to the ones after it
		}
		if total+add > maxBytes {
			break
		}
		total += add
		keep++
	}
	if keep == 0 {
		// Even the last line alone is over budget; keep its tail.
		last := lines[len(lines)-1]
		marker := fmt.Sprintf("[… %d earlier line(s) truncated, last line head dropped]\n", len(lines)-1)
		if budget := maxBytes - len(marker); budget > 0 && len(last) > budget {
			return marker + last[len(last)-budget:]
		}
		return marker + last
	}
	dropped := len(lines) - keep
	body := strings.Join(lines[len(lines)-keep:], "\n")
	if dropped == 0 {
		return body
	}
	return fmt.Sprintf("[… %d earlier line(s) truncated]\n", dropped) + body
}

// buildWarningsBlockRE matches the standalone build_warnings step block the
// build tool bodies (build-binary.sh, build-source.sh) emit after a
// successful build, through the end of its END-marker line.
var buildWarningsBlockRE = regexp.MustCompile(`(?ms)^ISPKG_STEP_BEGIN: build_warnings$.*?^ISPKG_STEP_END: build_warnings[^\n]*\n?`)

// cutBuildWarningsBlock removes the build_warnings block from out so the
// tool-level LogTail keeps showing the end of the build itself instead of a
// report that is already returned as a structured field. An unterminated
// block (the shell died mid-analysis) is left in place — the tail is then the
// best record of what happened.
func cutBuildWarningsBlock(out string) string {
	return buildWarningsBlockRE.ReplaceAllString(out, "")
}

// findReviewStep returns the first parsed step with the given name, or nil.
func findReviewStep(steps []reviewStep, name string) *reviewStep {
	for i := range steps {
		if steps[i].Name == name {
			return &steps[i]
		}
	}
	return nil
}

// Per-status LogTail budgets applied to the review report AFTER parsing. The
// parse-time cap (maxStepLogBytes) protects the gRPC transport; these protect
// the consumer: MCP clients truncate large tool results (a live run returned
// 265 KB and the agent's harness cut it after ~6 steps), so failures get the
// most room and healthy steps keep only their tail. Trims are tail-preserving
// (tailBytes), so steps should emit their most valuable summary output LAST.
const (
	trimFailLogBytes = 16 * 1024
	trimWarnLogBytes = 8 * 1024
	trimOKLogBytes   = 2 * 1024
)

// trimStepLogs applies the per-status budgets in place. Skipped steps carry
// their whole story in the summary, so their logs are dropped entirely.
func trimStepLogs(steps []reviewStep) {
	for i := range steps {
		var budget int
		switch steps[i].Status {
		case "fail":
			budget = trimFailLogBytes
		case "ok":
			budget = trimOKLogBytes
		case "skipped":
			budget = 0
		default: // warn, and anything unexpected
			budget = trimWarnLogBytes
		}
		if budget == 0 {
			steps[i].LogTail = ""
			continue
		}
		if len(steps[i].LogTail) > budget {
			steps[i].LogTail = tailBytes(strings.Split(steps[i].LogTail, "\n"), budget)
		}
	}
}

// severityRank orders statuses for the report: failures first, so a consumer
// that truncates the result still sees what broke before the cut.
func severityRank(status string) int {
	switch status {
	case "fail":
		return 0
	case "warn":
		return 1
	case "ok":
		return 3
	case "skipped":
		return 4
	default:
		return 2 // unknown: after the known-bad, before the known-good
	}
}

// sortStepsBySeverity reorders steps fail → warn → ok → skipped, keeping
// execution order within each class.
func sortStepsBySeverity(steps []reviewStep) {
	sort.SliceStable(steps, func(i, j int) bool {
		return severityRank(steps[i].Status) < severityRank(steps[j].Status)
	})
}

// stepDigest returns compact "name: summary" lines for every step with the
// given status — the truncation-proof failure record emitted ahead of the
// steps array.
func stepDigest(steps []reviewStep, status string) []string {
	var out []string
	for _, s := range steps {
		if s.Status != status {
			continue
		}
		if s.Summary != "" {
			out = append(out, s.Name+": "+s.Summary)
		} else {
			out = append(out, s.Name)
		}
	}
	return out
}

// overallReviewStatus aggregates per-step results: fail dominates, then warn,
// then ok. Skipped steps don't move the needle.
func overallReviewStatus(steps []reviewStep) string {
	overall := "ok"
	for _, s := range steps {
		switch s.Status {
		case "fail":
			return "fail"
		case "warn":
			overall = "warn"
		}
	}
	return overall
}

// tail returns the last n lines of s.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

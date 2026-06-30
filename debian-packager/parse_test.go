package main

import (
	"strings"
	"testing"
)

func TestFindArtifacts(t *testing.T) {
	out := `dpkg-deb: building package 'rocm-core' in '../build-area/rocm-core_7.2.4-1~exp1_amd64.deb'.
dpkg-genchanges --build=binary >../build-area/rocm-core_7.2.4-1~exp1_amd64.changes
some noise foo.txt and bar.log`
	got := findArtifacts(out)

	want := []string{
		"../build-area/rocm-core_7.2.4-1~exp1_amd64.changes",
		"../build-area/rocm-core_7.2.4-1~exp1_amd64.deb",
	}
	for _, w := range want {
		if !containsStr(got, w) {
			t.Fatalf("missing %q in %v", w, got)
		}
	}
	if containsStr(got, "foo.txt") || containsStr(got, "bar.log") {
		t.Fatalf("captured non-artifact: %v", got)
	}
	if c := pickExt(got, ".changes"); c != "../build-area/rocm-core_7.2.4-1~exp1_amd64.changes" {
		t.Fatalf("pickExt(.changes) = %q", c)
	}
}

func TestParseLintian(t *testing.T) {
	out := `E: rocm-core: no-changelog usr/share/doc/rocm-core/changelog.gz
W: rocm-core: binary-without-manpage usr/bin/rocm_agent_enumerator
I: rocm-core: hardening-no-fortify-functions usr/bin/foo
P: rocm-core: insane-line-length-in-source-file debian/rules:42`
	s := parseLintian(out)
	if s == nil {
		t.Fatal("nil summary")
	}
	if s.Errors != 1 || s.Warnings != 1 || s.Info != 1 || s.Pedantic != 1 {
		t.Fatalf("counts wrong: %+v", s)
	}
	if len(s.Tags) != 4 {
		t.Fatalf("tags = %v", s.Tags)
	}
	if s.Tags[0] != "E: no-changelog" {
		t.Fatalf("first tag = %q", s.Tags[0])
	}
}

func TestParseLintianNone(t *testing.T) {
	if s := parseLintian("nothing to see here\n"); s != nil {
		t.Fatalf("expected nil, got %+v", s)
	}
}

func TestParseWatchNewer(t *testing.T) {
	out := `uscan: Newer version available
<dehs>
<package>rocm-core</package>
<debian-uversion>7.2.4</debian-uversion>
<upstream-version>7.3.0</upstream-version>
<upstream-url>https://example/rocm-core-7.3.0.tar.gz</upstream-url>
<status>Newer version available</status>
</dehs>`
	w, err := parseWatch(out)
	if err != nil {
		t.Fatal(err)
	}
	if w.Package != "rocm-core" || w.CurrentVersion != "7.2.4" || w.UpstreamVersion != "7.3.0" {
		t.Fatalf("parsed = %+v", w)
	}
	if !w.NewerAvailable {
		t.Fatal("expected newer available")
	}
	if w.UpstreamURL == "" {
		t.Fatal("expected upstream url")
	}
}

func TestParseWatchUpToDate(t *testing.T) {
	out := `<dehs><package>p</package><debian-uversion>1.0</debian-uversion><upstream-version>1.0</upstream-version><status>up to date</status></dehs>`
	w, err := parseWatch(out)
	if err != nil {
		t.Fatal(err)
	}
	if w.NewerAvailable {
		t.Fatalf("expected up to date: %+v", w)
	}
}

func TestParseWatchErrors(t *testing.T) {
	out := "<dehs>\n<errors>No debian directories found</errors>\n</dehs>"
	w, err := parseWatch(out)
	if err != nil {
		t.Fatal(err)
	}
	if w.Errors != "No debian directories found" {
		t.Fatalf("errors = %q", w.Errors)
	}
	if w.NewerAvailable {
		t.Fatal("should not be newer on error")
	}
}

func TestParseWatchNoDehs(t *testing.T) {
	if _, err := parseWatch("uscan: error: something went wrong"); err == nil {
		t.Fatal("expected error when no DEHS block present")
	}
}

func TestTail(t *testing.T) {
	if got := tail("a\nb\nc\nd", 2); got != "c\nd" {
		t.Fatalf("tail = %q", got)
	}
	if got := tail("only", 5); got != "only" {
		t.Fatalf("tail short = %q", got)
	}
}

func TestRelayError(t *testing.T) {
	if err := relayError("log line\nnothing wrong here\n"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	out := "doing things\nISPKG_ERROR: orig tarball fetch failed (uscan --download-current-version)\nmore"
	err := relayError(out)
	if err == nil || !strings.Contains(err.Error(), "orig tarball fetch failed") {
		t.Fatalf("relayError = %v", err)
	}
	multi := "ISPKG_ERROR: first\nISPKG_ERROR: second\n"
	if err := relayError(multi); err == nil || !strings.Contains(err.Error(), "first") || !strings.Contains(err.Error(), "second") {
		t.Fatalf("expected both errors, got %v", err)
	}
}

func TestWarningMarkers(t *testing.T) {
	if w := warningMarkers("nothing\n"); w != nil {
		t.Fatalf("expected nil, got %v", w)
	}
	out := "ISPKG_WARNING: no binary packages to lint\nbuild log\nISPKG_WARNING: kept for debugging (exit 1): /tmp/x"
	w := warningMarkers(out)
	if len(w) != 2 || w[0] != "no binary packages to lint" {
		t.Fatalf("warnings = %v", w)
	}
}

func TestParseReviewSteps(t *testing.T) {
	out := `preamble noise the parser ignores
ISPKG_STEP_BEGIN: watch
uscan: Newer version available
ISPKG_STEP_STATUS: ok
ISPKG_STEP_SUMMARY: watch file works
ISPKG_STEP_END: watch exit=0
ISPKG_STEP_BEGIN: lintian_source
E: rocm-core: no-changelog
ISPKG_STEP_STATUS: warn
ISPKG_STEP_SUMMARY: lintian reported tags
ISPKG_STEP_HINT: agent: resolve every E
ISPKG_STEP_END: lintian_source exit=0
ISPKG_STEP_BEGIN: copyright_lrc
lrc binary not found
ISPKG_STEP_STATUS: skipped
ISPKG_STEP_SUMMARY: lrc not installed
ISPKG_STEP_END: copyright_lrc exit=0
ISPKG_STEP_BEGIN: control_wrap_sort
diff output line
ISPKG_STEP_END: control_wrap_sort exit=1
`
	steps := parseReviewSteps(out, 10)
	if len(steps) != 4 {
		t.Fatalf("expected 4 steps, got %d: %+v", len(steps), steps)
	}

	if steps[0].Name != "watch" || steps[0].Status != "ok" || steps[0].Exit != 0 {
		t.Errorf("step 0 = %+v", steps[0])
	}
	if !strings.Contains(steps[0].LogTail, "uscan: Newer") {
		t.Errorf("step 0 log missing uscan line: %q", steps[0].LogTail)
	}
	if strings.Contains(steps[0].LogTail, "ISPKG_STEP_") {
		t.Errorf("step 0 log still contains markers: %q", steps[0].LogTail)
	}

	if steps[1].Status != "warn" || steps[1].Summary != "lintian reported tags" {
		t.Errorf("step 1 = %+v", steps[1])
	}
	if steps[1].AgentHint != "agent: resolve every E" {
		t.Errorf("step 1 hint = %q", steps[1].AgentHint)
	}

	if steps[2].Status != "skipped" {
		t.Errorf("step 2 status = %q, want skipped", steps[2].Status)
	}

	// last step: no STATUS marker, non-zero exit → defaults to fail
	if steps[3].Status != "fail" || steps[3].Exit != 1 {
		t.Errorf("step 3 = %+v (expected fail/exit=1)", steps[3])
	}
}

func TestParseReviewStepsPartialBlock(t *testing.T) {
	// BEGIN with no matching END (script crashed mid-step) must still emit
	// a step entry, not be silently dropped.
	out := `ISPKG_STEP_BEGIN: watch
some log
ISPKG_STEP_BEGIN: lintian_source
ISPKG_STEP_STATUS: ok
ISPKG_STEP_END: lintian_source exit=0
`
	steps := parseReviewSteps(out, 10)
	if len(steps) != 2 {
		t.Fatalf("expected 2 steps, got %d: %+v", len(steps), steps)
	}
	if steps[0].Name != "watch" || steps[0].Status != "fail" || steps[0].Exit != -1 {
		t.Errorf("partial step = %+v (expected fail/exit=-1)", steps[0])
	}
	if steps[1].Name != "lintian_source" || steps[1].Status != "ok" {
		t.Errorf("second step = %+v", steps[1])
	}
}

func TestParseReviewStepsEmpty(t *testing.T) {
	if s := parseReviewSteps("no markers at all\n", 10); len(s) != 0 {
		t.Fatalf("expected no steps, got %+v", s)
	}
}

func TestOverallReviewStatus(t *testing.T) {
	cases := []struct {
		name  string
		steps []reviewStep
		want  string
	}{
		{"all ok", []reviewStep{{Status: "ok"}, {Status: "ok"}}, "ok"},
		{"skipped ignored", []reviewStep{{Status: "ok"}, {Status: "skipped"}}, "ok"},
		{"any warn", []reviewStep{{Status: "ok"}, {Status: "warn"}, {Status: "ok"}}, "warn"},
		{"fail dominates", []reviewStep{{Status: "warn"}, {Status: "fail"}, {Status: "ok"}}, "fail"},
		{"empty", nil, "ok"},
	}
	for _, c := range cases {
		if got := overallReviewStatus(c.steps); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

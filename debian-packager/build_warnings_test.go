package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// extractAwkProg pulls the ISPKG_WARNINGS_AWK='...' body out of
// scripts/prelude.sh (the shared analyzer used by review.sh's
// ppa_build_warnings step and build-binary.sh's build_warnings block).
// The delimiters are the exact strings `ISPKG_WARNINGS_AWK='\n` and a
// column-0 `\n'\n`. If the shape changes, this test fails loudly rather
// than silently running the wrong program.
func extractAwkProg(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("scripts/prelude.sh")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	const begin = "ISPKG_WARNINGS_AWK='\n"
	i := strings.Index(src, begin)
	if i < 0 {
		t.Fatal("ISPKG_WARNINGS_AWK=' opening marker not found in scripts/prelude.sh")
	}
	rest := src[i+len(begin):]
	j := strings.Index(rest, "\n'\n")
	if j < 0 {
		t.Fatal("ISPKG_WARNINGS_AWK closing marker not found in scripts/prelude.sh")
	}
	return rest[:j+1] // keep the trailing newline before the '
}

func runAwk(t *testing.T, prog string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("awk not on PATH")
	}
	cmd := exec.Command("awk", append([]string{"-f", "-"}, args...)...)
	cmd.Stdin = strings.NewReader(prog)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("awk failed: %v\nstderr: %s", err, errb.String())
	}
	return out.String()
}

// TestBuildWarningsAwkTotals is the canonical, order-independent regression
// test. `mode=totals` prints one deterministic line summarising each bucket,
// so we can string-compare exactly. The expected line encodes:
//
//	S=3        — 3 (var, pkg) substvar pairs after dedup
//	P=2x1      — 2 total plugin-symbol matches, 1 unique symbol
//	U=2        — 2 unique (path, lib) useless-dep pairs
//	O=2        — 2 unique other-dpkg warning lines (a dup collapses)
//	D=1        — 1 dh_* warning line
//	C=1        — 1 CMake Warning header
//
// The fixture at testdata/ppa_build_warnings/build.log.sample is crafted
// so each category has at least one hit AND at least one intentional
// non-match that must be rejected.
func TestBuildWarningsAwkTotals(t *testing.T) {
	prog := extractAwkProg(t)
	got := runAwk(t, prog, "-v", "mode=totals", "testdata/ppa_build_warnings/build.log.sample")
	want := "S=3 P=2x1 U=2 O=2 D=1 C=1\n"
	if got != want {
		t.Errorf("totals mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestBuildWarningsAwkCategories exercises the human-readable output.
// Awk's `for (k in array)` order is undefined, so we assert on presence /
// absence of substrings, not full-string equality. This still catches
// regressions in what got matched / what noise slipped through.
func TestBuildWarningsAwkCategories(t *testing.T) {
	prog := extractAwkProg(t)
	got := runAwk(t, prog, "testdata/ppa_build_warnings/build.log.sample")

	mustContain := []string{
		// Section headers
		"=== undefined substvars (3) ===",
		"=== shlibdeps: unresolvable plugin refs (2 total, 1 unique symbol) ===",
		"=== shlibdeps: useless dependencies (2 unique) ===",
		"=== other dpkg warnings (2 unique) ===",
		"=== dh_ warnings (1 unique) ===",
		"=== CMake warnings (1 unique) ===",
		// Substvar content: the same var used by both packages must
		// collapse into one line naming both.
		"${dep:devlibs}",
		"clang-rocm",
		"libclang-rocm-dev",
		"${dep:devlibs-objc}",
		// Plugin symbol + count
		"LLVM_23.0@LLVM_23.0",
		"x 2",
		// Useless deps
		"libgcc_s.so.1",
		"libz.so.1",
		"libRemarks.so.23.0rocm7.2.14~",
		"clang-tblgen",
		// dh_ warning
		"dh_shlibdeps: warning: Ignoring unknown option --no-something",
		// other dpkg warnings (dpkg-source dup collapsed, dpkg-buildpackage kept)
		"ignoring deletion of file .gitignore",
		"not signing UNRELEASED build",
		// CMake header
		"CMake Warning at CMakeLists.txt:17 (find_package):",
	}
	for _, s := range mustContain {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in awk output:\n%s", s, got)
		}
	}

	// Noise lines from the fixture that MUST NOT be reported:
	//   - `-- CMake Warning:` (not anchored to line start)
	//   - `dpkg-shlibdeps: warning: symbol foo not found` (not the plugin
	//     variant, and shlibdeps is deliberately NOT in the other-dpkg bucket)
	//   - `dh_installdocs: file … not found` (no `warning:`)
	//   - `dpkg-source: info: …` (info, not warning)
	mustNotContain := []string{
		"fake nested cmake warning",
		"symbol foo not found",
		"dh_installdocs: file /nonexistent",
		"building rocm-core using existing",
	}
	for _, s := range mustNotContain {
		if strings.Contains(got, s) {
			t.Errorf("noise leaked into awk output — contains %q:\n%s", s, got)
		}
	}
}

// TestBuildBodiesEmitBuildWarningsStep guards against drift between the
// build tool bodies' build_warnings blocks, the prelude helpers they call,
// and the Go side that parses the block by name.
func TestBuildBodiesEmitBuildWarningsStep(t *testing.T) {
	for _, script := range []string{"scripts/build-binary.sh", "scripts/build-source.sh"} {
		body, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range []string{
			"ISPKG_STEP_BEGIN: build_warnings",
			"ISPKG_STEP_END: build_warnings exit=0",
			"build_warnings_report",
			"build_warnings_totals",
			"$ISPKG_WARNINGS_NONE",
		} {
			if !strings.Contains(string(body), marker) {
				t.Errorf("%s missing %q", script, marker)
			}
		}
	}
	prelude, err := os.ReadFile("scripts/prelude.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range []string{
		"build_warnings_report()",
		"build_warnings_totals()",
		"ISPKG_WARNINGS_NONE=",
		"status()",
		"summary()",
		"hint()",
	} {
		if !strings.Contains(string(prelude), def) {
			t.Errorf("scripts/prelude.sh missing definition %q", def)
		}
	}
}

// TestScriptsParse runs `sh -n` over the prelude plus each tool body exactly
// as pipelineCommand assembles them, so a shell syntax error is caught at
// test time instead of on the first builder invocation.
func TestScriptsParse(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	bodies := []string{buildSourceBody, buildBinaryBody, checkWatchBody, lintBody, reviewBody}
	names := []string{"build-source.sh", "build-binary.sh", "check-watch.sh", "lint.sh", "review (assembled)"}
	for i, body := range bodies {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(preludeScript + "\n" + body)
		var errb bytes.Buffer
		cmd.Stderr = &errb
		if err := cmd.Run(); err != nil {
			t.Errorf("prelude+%s does not parse: %v\n%s", names[i], err, errb.String())
		}
	}
}

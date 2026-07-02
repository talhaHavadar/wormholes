package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// extractAwkProg pulls the awk_prog='...' body out of scripts/review.sh.
// The delimiters are the exact strings `awk_prog='\n` and `\n'` at column
// 4 (inside step_ppa_build_warnings). If the shape changes, this test
// fails loudly rather than silently running the wrong program.
func extractAwkProg(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("scripts/review.sh")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	const begin = "awk_prog='\n"
	i := strings.Index(src, begin)
	if i < 0 {
		t.Fatal("awk_prog=' opening marker not found in scripts/review.sh")
	}
	rest := src[i+len(begin):]
	j := strings.Index(rest, "\n'\n")
	if j < 0 {
		t.Fatal("awk_prog closing marker not found in scripts/review.sh")
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
//	D=1        — 1 dh_* warning line
//	C=1        — 1 CMake Warning header
//
// The fixture at testdata/ppa_build_warnings/build.log.sample is crafted
// so each category has at least one hit AND at least one intentional
// non-match that must be rejected.
func TestBuildWarningsAwkTotals(t *testing.T) {
	prog := extractAwkProg(t)
	got := runAwk(t, prog, "-v", "mode=totals", "testdata/ppa_build_warnings/build.log.sample")
	want := "S=3 P=2x1 U=2 D=1 C=1\n"
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
	//   - `dpkg-shlibdeps: warning: symbol foo not found` (not the plugin variant)
	//   - `dh_installdocs: file … not found` (no `warning:`)
	mustNotContain := []string{
		"fake nested cmake warning",
		"symbol foo not found",
		"dh_installdocs: file /nonexistent",
	}
	for _, s := range mustNotContain {
		if strings.Contains(got, s) {
			t.Errorf("noise leaked into awk output — contains %q:\n%s", s, got)
		}
	}
}

package main

import (
	"os"
	"strings"
	"testing"
)

// TestReviewShellHasPPABuildWarnings guards against drift between the
// ENABLED_STEPS list at the top of scripts/review.sh and the step_*()
// function definitions below. review.sh itself refuses to run when the
// two disagree, but that check only fires at tool-call time on the builder;
// this test catches the mismatch at `go test` time.
func TestReviewShellHasPPABuildWarnings(t *testing.T) {
	b, err := os.ReadFile("scripts/review.sh")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "\n  ppa_build_warnings\n") {
		t.Error("ENABLED_STEPS missing ppa_build_warnings")
	}
	if !strings.Contains(src, "step_ppa_build_warnings()") {
		t.Error("step_ppa_build_warnings() not defined")
	}
}

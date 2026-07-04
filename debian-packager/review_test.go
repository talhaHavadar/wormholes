package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// enabledSteps parses the ENABLED_STEPS list out of review/framework.sh.
func enabledSteps(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("scripts/review/framework.sh")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)\nENABLED_STEPS="\n(.*?)"`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("ENABLED_STEPS block not found in scripts/review/framework.sh")
	}
	return strings.Fields(m[1])
}

// TestReviewStepsMatchEnabledList guards against drift between the
// ENABLED_STEPS list in framework.sh and the steps/*.sh files, in both
// directions, and checks each file defines its step_<name>() function.
// The runner also refuses to start on a missing function, but that check
// only fires at tool-call time on the builder; this catches it at
// `go test` time.
func TestReviewStepsMatchEnabledList(t *testing.T) {
	steps := enabledSteps(t)
	if len(steps) == 0 {
		t.Fatal("ENABLED_STEPS is empty")
	}
	listed := map[string]bool{}
	for _, s := range steps {
		listed[s] = true
	}

	entries, err := os.ReadDir("scripts/review/steps")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".sh")
		files[name] = true
		src, err := os.ReadFile("scripts/review/steps/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "step_"+name+"() {") {
			t.Errorf("steps/%s does not define step_%s()", e.Name(), name)
		}
	}

	for s := range listed {
		if !files[s] {
			t.Errorf("ENABLED_STEPS lists %q but scripts/review/steps/%s.sh does not exist", s, s)
		}
	}
	for f := range files {
		if !listed[f] {
			t.Errorf("scripts/review/steps/%s.sh exists but is not in ENABLED_STEPS", f)
		}
	}
}

// TestReviewResultJSONOrder pins the field order the truncation-proofing
// relies on: MCP clients cut large tool results from the END, so the
// failed/warned digest must serialize before the steps array.
func TestReviewResultJSONOrder(t *testing.T) {
	b, err := json.Marshal(reviewResult{
		OverallStatus: "fail",
		FailedSteps:   []string{"watch: uscan failed"},
		WarnedSteps:   []string{"signed_tags: no pgp verification"},
		Steps:         []reviewStep{{Name: "watch", Status: "fail"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	iStatus := strings.Index(s, `"overall_status"`)
	iFailed := strings.Index(s, `"failed_steps"`)
	iWarned := strings.Index(s, `"warned_steps"`)
	iSteps := strings.Index(s, `"steps"`)
	if iStatus < 0 || iFailed < 0 || iWarned < 0 || iSteps < 0 {
		t.Fatalf("missing fields in %s", s)
	}
	if !(iStatus < iFailed && iFailed < iWarned && iWarned < iSteps) {
		t.Errorf("digest fields must precede steps: %s", s)
	}
}

// TestAssembledReviewBody sanity-checks the Go-side assembly: framework
// first, runner last, every step function in between.
func TestAssembledReviewBody(t *testing.T) {
	if !strings.HasPrefix(reviewBody, "# review/framework.sh") {
		t.Error("reviewBody does not start with framework.sh")
	}
	if !strings.Contains(reviewBody, "# review/runner.sh") {
		t.Error("reviewBody missing runner.sh")
	}
	for _, s := range enabledSteps(t) {
		fn := "step_" + s + "() {"
		i := strings.Index(reviewBody, fn)
		if i < 0 {
			t.Errorf("reviewBody missing %s", fn)
			continue
		}
		if i > strings.Index(reviewBody, "# review/runner.sh") {
			t.Errorf("%s defined after the runner", fn)
		}
	}
}

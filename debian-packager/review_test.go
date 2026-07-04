package main

import (
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

package main

import (
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPrepareJobInjectsReserveData(t *testing.T) {
	base := []byte("job_queue: maas-x86\nprovision_data:\n  distro: noble\n")
	out, err := prepareJob(base, config{SSHKeys: []string{"gh:talhaHavadar", "lp:talha"}, ReserveTimeoutSecs: 7200})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["job_queue"] != "maas-x86" {
		t.Fatalf("job_queue lost: %v", doc["job_queue"])
	}
	rd, ok := doc["reserve_data"].(map[string]any)
	if !ok {
		t.Fatalf("reserve_data missing/wrong type: %T", doc["reserve_data"])
	}
	if rd["timeout"] != 7200 {
		t.Fatalf("timeout = %v", rd["timeout"])
	}
	keys, ok := rd["ssh_keys"].([]any)
	if !ok || len(keys) != 2 || keys[0] != "gh:talhaHavadar" {
		t.Fatalf("ssh_keys = %v", rd["ssh_keys"])
	}
}

func TestPrepareJobKeepsExistingKeys(t *testing.T) {
	base := []byte("job_queue: q\nreserve_data:\n  ssh_keys:\n    - lp:someone\n")
	out, err := prepareJob(base, config{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "lp:someone") {
		t.Fatalf("existing ssh key dropped: %s", out)
	}
}

// TestPrepareJobFromConfigOnly builds a job with no base file, entirely from
// direct config fields.
func TestPrepareJobFromConfigOnly(t *testing.T) {
	out, err := prepareJob(nil, config{
		JobQueue:      "maas-x86",
		ProvisionData: map[string]any{"distro": "noble"},
		SSHKeys:       []string{"gh:talha"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["job_queue"] != "maas-x86" {
		t.Fatalf("job_queue = %v", doc["job_queue"])
	}
	pd, ok := doc["provision_data"].(map[string]any)
	if !ok || pd["distro"] != "noble" {
		t.Fatalf("provision_data = %v", doc["provision_data"])
	}
}

// TestPrepareJobConfigOverridesFile checks config takes priority while the job
// file fills in the rest: job_queue is replaced, provision_data merges key by
// key (config distro wins, file-only disks survive).
func TestPrepareJobConfigOverridesFile(t *testing.T) {
	base := []byte("job_queue: from-file\nprovision_data:\n  distro: jammy\n  disks: [sda]\n")
	out, err := prepareJob(base, config{
		JobQueue:      "from-config",
		ProvisionData: map[string]any{"distro": "noble"},
		SSHKeys:       []string{"gh:talha"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["job_queue"] != "from-config" {
		t.Fatalf("job_queue not overridden: %v", doc["job_queue"])
	}
	pd, _ := doc["provision_data"].(map[string]any)
	if pd["distro"] != "noble" {
		t.Fatalf("distro not overridden: %v", pd["distro"])
	}
	if _, ok := pd["disks"]; !ok {
		t.Fatalf("file-only provision_data key dropped: %v", pd)
	}
}

func TestPrepareJobErrors(t *testing.T) {
	if _, err := prepareJob([]byte("provision_data: {distro: noble}\n"), config{SSHKeys: []string{"gh:x"}, ReserveTimeoutSecs: 1}); err == nil {
		t.Fatal("expected error on missing job_queue")
	}
	if _, err := prepareJob([]byte("job_queue: q\n"), config{}); err == nil {
		t.Fatal("expected error on missing ssh_keys")
	}
}

func TestParseConfigRequiresJobSource(t *testing.T) {
	// Neither a job file nor a direct job_queue: rejected.
	if _, err := parseConfig([]byte(`{"ssh_keys":["gh:x"]}`)); err == nil {
		t.Fatal("expected error when neither job_file nor job_queue is set")
	}
	// job_queue alone (no file) is enough to pass config validation.
	if _, err := parseConfig([]byte(`{"job_queue":"maas-x86"}`)); err != nil {
		t.Fatalf("job_queue should satisfy config validation: %v", err)
	}
	// A job file alone is still fine.
	if _, err := parseConfig([]byte(`{"job_file":"/tmp/job.yaml"}`)); err != nil {
		t.Fatalf("job_file should satisfy config validation: %v", err)
	}
}

func TestParseJobID(t *testing.T) {
	id, err := parseJobID("Job submitted successfully!\njob_id: 1b4e28ba-2fa1-11d2-883f-0016d3cca427\n")
	if err != nil {
		t.Fatal(err)
	}
	if id != "1b4e28ba-2fa1-11d2-883f-0016d3cca427" {
		t.Fatalf("id = %q", id)
	}
	if _, err := parseJobID("no uuid here"); err == nil {
		t.Fatal("expected error when no uuid")
	}
}

func TestParseSSHInfoCommandLine(t *testing.T) {
	out := `Waiting for job to start...
*** TESTFLINGER SYSTEM RESERVED ***
You can now connect to the system with:
ssh ubuntu@10.102.156.15
The system will be reserved until ...`
	tgt, ok := parseSSHInfo(out, nil)
	if !ok || tgt.User != "ubuntu" || tgt.Host != "10.102.156.15" {
		t.Fatalf("parsed = %+v ok=%v", tgt, ok)
	}
}

func TestParseSSHInfoBare(t *testing.T) {
	out := "Reserved. Connect with ubuntu@192.168.1.42 (expires in 6h)"
	tgt, ok := parseSSHInfo(out, nil)
	if !ok || tgt.User != "ubuntu" || tgt.Host != "192.168.1.42" {
		t.Fatalf("parsed = %+v ok=%v", tgt, ok)
	}
}

func TestParseSSHInfoLastWins(t *testing.T) {
	out := "ssh ubuntu@1.1.1.1\n...later...\nssh ubuntu@2.2.2.2\n"
	tgt, _ := parseSSHInfo(out, nil)
	if tgt.Host != "2.2.2.2" {
		t.Fatalf("expected last match, got %s", tgt.Host)
	}
}

func TestParseSSHInfoCustom(t *testing.T) {
	re := regexp.MustCompile(`reserved=(\w+)@([\d.]+)`)
	tgt, ok := parseSSHInfo("status reserved=root@10.0.0.5 done", re)
	if !ok || tgt.User != "root" || tgt.Host != "10.0.0.5" {
		t.Fatalf("custom parse = %+v ok=%v", tgt, ok)
	}
}

func TestParseSSHInfoNone(t *testing.T) {
	if _, ok := parseSSHInfo("still provisioning, please wait", nil); ok {
		t.Fatal("should not match while provisioning")
	}
}

func TestRemoteShellCommand(t *testing.T) {
	got := remoteShellCommand("/work/pkg", map[string]string{"CONTAINED_CONTAINER_RUNTIME": "podman"},
		[]string{"contained", "-c", "img", "--", "sbuild", "-d", "trixie"})
	want := `cd '/work/pkg' && CONTAINED_CONTAINER_RUNTIME='podman' 'contained' '-c' 'img' '--' 'sbuild' '-d' 'trixie'`
	if got != want {
		t.Fatalf("remoteShellCommand =\n  %s\nwant\n  %s", got, want)
	}
}

func TestRemoteShellCommandNoDir(t *testing.T) {
	got := remoteShellCommand("", nil, []string{"uname", "-a"})
	if got != `'uname' '-a'` {
		t.Fatalf("got %q", got)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Fatalf("shellQuote = %q", got)
	}
}

func TestLastLine(t *testing.T) {
	if got := lastLine("a\nb\nc\n"); got != "c" {
		t.Fatalf("lastLine = %q", got)
	}
}

type discardSink struct{}

func (discardSink) Stdout(_ []byte) {}
func (discardSink) Stderr(_ []byte) {}
func (discardSink) SetExit(_ int)   {}

// TestSSHFailWatcher covers the "signal the link dead" heuristic: exit 255
// with an unreachable-host stderr fragment must trip; a plain non-zero remote
// command must not.
func TestSSHFailWatcher(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		exit   int
		want   bool
	}{
		{"connection refused, exit 255", "ssh: connect to host 10.0.0.5 port 22: Connection refused\r\n", 255, true},
		{"no route to host, exit 255", "ssh: connect to host 10.0.0.5 port 22: No route to host\r\n", 255, true},
		{"connection timed out, exit 255", "ssh: connect to host 10.0.0.5 port 22: Connection timed out\r\n", 255, true},
		{"reset by peer, exit 255", "packet_write_wait: Connection reset by peer\r\n", 255, true},
		{"remote command failed, exit 1", "make: *** [Makefile:12: build] Error 1\r\n", 1, false},
		{"connection refused but exit 0", "warn: connection refused (ignored)\r\n", 0, false},
		{"exit 255 but stderr does not look like a network issue", "shell exited with 255\r\n", 255, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &sshFailWatcher{sink: discardSink{}}
			w.Stderr([]byte(tc.stderr))
			w.SetExit(tc.exit)
			if got := w.sshUnreachable(); got != tc.want {
				t.Fatalf("sshUnreachable = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestSSHFailWatcherBoundedTail asserts stderr accumulation is capped so a
// chatty command cannot drive the watcher's memory footprint unbounded.
func TestSSHFailWatcherBoundedTail(t *testing.T) {
	w := &sshFailWatcher{sink: discardSink{}}
	// Ten times the tail budget, all benign output.
	blob := strings.Repeat("x", sshFailStderrTailBytes*10)
	w.Stderr([]byte(blob))
	if len(w.stderrTail) > sshFailStderrTailBytes {
		t.Fatalf("stderrTail grew to %d bytes; want <= %d", len(w.stderrTail), sshFailStderrTailBytes)
	}
	// The unreachable marker at the very end of the stream must still be
	// visible even after truncation, so a long build followed by ssh dying
	// still trips the heuristic.
	w2 := &sshFailWatcher{sink: discardSink{}}
	w2.Stderr([]byte(strings.Repeat("x", sshFailStderrTailBytes)))
	w2.Stderr([]byte("ssh: connect to host 10.0.0.5 port 22: Connection refused\r\n"))
	w2.SetExit(255)
	if !w2.sshUnreachable() {
		t.Fatal("late connection-refused should still trip after long benign stderr")
	}
}

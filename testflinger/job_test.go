package main

import (
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPrepareJobInjectsReserveData(t *testing.T) {
	base := []byte("job_queue: maas-x86\nprovision_data:\n  distro: noble\n")
	out, err := prepareJob(base, []string{"gh:talhaHavadar", "lp:talha"}, 7200)
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
	out, err := prepareJob(base, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "lp:someone") {
		t.Fatalf("existing ssh key dropped: %s", out)
	}
}

func TestPrepareJobErrors(t *testing.T) {
	if _, err := prepareJob([]byte("provision_data: {distro: noble}\n"), []string{"gh:x"}, 1); err == nil {
		t.Fatal("expected error on missing job_queue")
	}
	if _, err := prepareJob([]byte("job_queue: q\n"), nil, 0); err == nil {
		t.Fatal("expected error on missing ssh_keys")
	}
}

func TestLooksLikeMAAS(t *testing.T) {
	if !looksLikeMAAS([]byte("job_queue: q\nprovision_data:\n  distro: noble\n")) {
		t.Fatal("distro job should look like MAAS")
	}
	if looksLikeMAAS([]byte("job_queue: q\nprovision_data:\n  url: http://img\n")) {
		t.Fatal("url job should not look like MAAS")
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

package main

import (
	"testing"
	"time"
)

// Fixtures below are lifted from real testflinger-cli output (snap 20260420,
// queue megatron) so the parsers are tested against the wire format we
// actually get, not a guess.

const queueStatusLiveJSON = `{
  "queue": "megatron",
  "agents": [
    {
      "name": "megatron",
      "status": "reserve"
    }
  ],
  "jobs_waiting": []
}`

const jobsListLive = `Job ID                                         Submission Time  Queue
-------------------------------------------------------------------------------
f9bd02ae-109e-4359-8d7b-34cd3b1576e9           Thu Jul 02 20:59 megatron
7eca47fa-9e50-4123-ab38-31f549877215           Fri Jul 03 07:34 megatron
da97169f-c90d-4dff-a9db-94c7ee2c8fab           Sat Jul 04 19:58 optimusprime
94aa9d70-36f0-4d6f-a159-96fca4ffad80           Sat Jul 04 20:46 megatron`

const reserveLogLive = `**************************************************
* Starting testflinger reserve phase on megatron *
**************************************************
2026-07-04 21:12:04,060 megatron INFO: DEVICE CONNECTOR: BEGIN reservation
2026-07-04 21:12:04,503 megatron INFO: DEVICE CONNECTOR: Successfully imported key: gh:talhaHavadar
/usr/bin/ssh-copy-id: INFO: Source of key(s) to be installed: "key.pub"

Number of key(s) added: 11

Now try logging into the machine, with:   "ssh -o 'StrictHostKeyChecking=no' -o 'UserKnownHostsFile=/dev/null' 'ubuntu@10.241.6.57'"
and check to make sure that only the key(s) you wanted were added.

*** TESTFLINGER SYSTEM RESERVED ***
You can now connect to ubuntu@10.241.6.57
Current time:           [2026-07-04T21:12:04.969018+00:00]
Reservation expires at: [2026-07-05T03:12:04.969077+00:00]
Reservation will automatically timeout in 21600 seconds
To end the reservation sooner use: testflinger-cli cancel 94aa9d70-36f0-4d6f-a159-96fca4ffad80
Last Fragment Number: 37`

const resultsLiveJSON = `{
  "device_info": {
    "agent_name": "megatron",
    "device_ip": "10.241.6.57"
  },
  "job_state": "reserve",
  "provision_output": "provision log here",
  "provision_status": 0,
  "reserve_output": "reserve log here",
  "reserve_status": 0,
  "setup_output": "setup log here",
  "setup_status": 0
}`

const showLiveJSON = `{
    "exclude_agents": [],
    "job_id": "94aa9d70-36f0-4d6f-a159-96fca4ffad80",
    "job_queue": "megatron",
    "provision_data": {
        "distro": "resolute"
    },
    "reserve_data": {
        "ssh_keys": [
            "gh:talhaHavadar"
        ],
        "timeout": 21600
    }
}`

func TestParseQueueStatusLive(t *testing.T) {
	qs, err := parseQueueStatus([]byte(queueStatusLiveJSON))
	if err != nil {
		t.Fatal(err)
	}
	if qs.Queue != "megatron" {
		t.Fatalf("queue = %q", qs.Queue)
	}
	if len(qs.Agents) != 1 || qs.Agents[0].Name != "megatron" || qs.Agents[0].Status != "reserve" {
		t.Fatalf("agents = %+v", qs.Agents)
	}
	if len(qs.JobsWaiting) != 0 {
		t.Fatalf("jobs waiting = %v", qs.JobsWaiting)
	}
}

// TestParseQueueStatusWaitingShapes: jobs_waiting has only been observed
// empty, so tolerate both plain ids and objects carrying a job_id.
func TestParseQueueStatusWaitingShapes(t *testing.T) {
	qs, err := parseQueueStatus([]byte(`{"queue":"q","agents":[],"jobs_waiting":["id-1",{"job_id":"id-2"},42]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(qs.JobsWaiting) != 3 || qs.JobsWaiting[0] != "id-1" || qs.JobsWaiting[1] != "id-2" {
		t.Fatalf("jobs waiting = %v", qs.JobsWaiting)
	}
}

func TestClassifyQueue(t *testing.T) {
	agent := func(status string) queueAgent { return queueAgent{Name: "a1", Status: status} }
	cases := []struct {
		name       string
		qs         queueStatus
		ours       map[string]bool
		waitIfBusy bool
		proceed    bool
	}{
		{"free agent", queueStatus{Agents: []queueAgent{agent("waiting")}}, nil, false, true},
		{"no agents", queueStatus{}, nil, false, false},
		{"all offline", queueStatus{Agents: []queueAgent{agent("offline"), agent("maintenance")}}, nil, false, false},
		{"busy, nothing waiting", queueStatus{Agents: []queueAgent{agent("reserve")}}, nil, false, true},
		{"busy, others waiting", queueStatus{Agents: []queueAgent{agent("test")}, JobsWaiting: []string{"x"}}, nil, false, false},
		{"busy, others waiting, wait_if_busy", queueStatus{Agents: []queueAgent{agent("test")}, JobsWaiting: []string{"x"}}, nil, true, true},
		{"busy, only our job waiting", queueStatus{Agents: []queueAgent{agent("test")}, JobsWaiting: []string{"mine"}}, map[string]bool{"mine": true}, false, true},
		{"one free among busy", queueStatus{Agents: []queueAgent{agent("provision"), agent("waiting")}, JobsWaiting: []string{"x"}}, nil, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := classifyQueue(tc.qs, tc.ours, tc.waitIfBusy)
			if v.Proceed != tc.proceed {
				t.Fatalf("proceed = %v (%s); want %v", v.Proceed, v.Reason, tc.proceed)
			}
			if v.Reason == "" {
				t.Fatal("verdict must always carry a reason")
			}
		})
	}
}

func TestParseJobsList(t *testing.T) {
	rows := parseJobsList(jobsListLive)
	if len(rows) != 4 {
		t.Fatalf("rows = %d: %+v", len(rows), rows)
	}
	if rows[0].ID != "f9bd02ae-109e-4359-8d7b-34cd3b1576e9" || rows[0].Queue != "megatron" {
		t.Fatalf("first row = %+v", rows[0])
	}
	if rows[2].Queue != "optimusprime" {
		t.Fatalf("third row queue = %q", rows[2].Queue)
	}
	if rows[3].ID != "94aa9d70-36f0-4d6f-a159-96fca4ffad80" {
		t.Fatalf("last row = %+v", rows[3])
	}
	if got := parseJobsList("no jobs here\n"); len(got) != 0 {
		t.Fatalf("garbage parsed as rows: %+v", got)
	}
}

func TestParseResultsLive(t *testing.T) {
	jr, err := parseResults([]byte(resultsLiveJSON))
	if err != nil {
		t.Fatal(err)
	}
	if jr.DeviceIP != "10.241.6.57" {
		t.Fatalf("device ip = %q", jr.DeviceIP)
	}
	if jr.JobState != "reserve" {
		t.Fatalf("job state = %q", jr.JobState)
	}
	if pr := jr.Phases["provision"]; pr.Status != 0 || pr.Output != "provision log here" {
		t.Fatalf("provision phase = %+v", pr)
	}
	if _, _, ok := failedPhase(jr); ok {
		t.Fatal("no phase failed, but failedPhase found one")
	}
}

func TestFailedPhaseOrdering(t *testing.T) {
	jr := jobResults{Phases: map[string]phaseResult{
		"provision": {Status: 1, Output: "maas deploy failed"},
		"reserve":   {Status: 2, Output: "never ran"},
	}}
	name, pr, ok := failedPhase(jr)
	if !ok || name != "provision" || pr.Output != "maas deploy failed" {
		t.Fatalf("failedPhase = %q %+v %v", name, pr, ok)
	}
}

func TestParseReserveExpiry(t *testing.T) {
	exp, ok := parseReserveExpiry(reserveLogLive)
	if !ok {
		t.Fatal("expiry not found in real reserve log")
	}
	want := time.Date(2026, 7, 5, 3, 12, 4, 969077000, time.UTC)
	if !exp.Equal(want) {
		t.Fatalf("expiry = %s; want %s", exp, want)
	}
	if _, ok := parseReserveExpiry("still provisioning"); ok {
		t.Fatal("expiry parsed from log without one")
	}
}

// TestParseSSHInfoRealReserveLog validates the fallback SSH-info parse
// against the genuine reserve-phase log — the regexes were previously only
// tested against invented output.
func TestParseSSHInfoRealReserveLog(t *testing.T) {
	tgt, ok := parseSSHInfo(reserveLogLive, nil)
	if !ok || tgt.User != "ubuntu" || tgt.Host != "10.241.6.57" {
		t.Fatalf("parsed = %+v ok=%v", tgt, ok)
	}
}

func TestJobSpecMatches(t *testing.T) {
	want := jobSpec{
		Queue:         "megatron",
		ProvisionData: map[string]any{"distro": "resolute"},
		SSHKeys:       []string{"gh:talhaHavadar"},
	}
	if !jobSpecMatches([]byte(showLiveJSON), want) {
		t.Fatal("real show JSON should match its own spec")
	}

	other := want
	other.ProvisionData = map[string]any{"distro": "noble"}
	if jobSpecMatches([]byte(showLiveJSON), other) {
		t.Fatal("distro mismatch must not match")
	}

	other = want
	other.Queue = "optimusprime"
	if jobSpecMatches([]byte(showLiveJSON), other) {
		t.Fatal("queue mismatch must not match")
	}

	other = want
	other.SSHKeys = []string{"gh:talhaHavadar", "lp:someoneelse"}
	if jobSpecMatches([]byte(showLiveJSON), other) {
		t.Fatal("a key we need but the job lacks must not match")
	}

	// The candidate having extra keys is fine — we can still log in.
	other = want
	other.SSHKeys = nil
	if !jobSpecMatches([]byte(showLiveJSON), other) {
		t.Fatal("candidate with superset of keys should match")
	}
}

// TestJobSpecMatchesNumbers: our wanted provision_data can come from YAML
// (ints) while show output is JSON (float64); the comparison must not care.
func TestJobSpecMatchesNumbers(t *testing.T) {
	show := []byte(`{"job_queue":"q","provision_data":{"distro":"noble","disks":2},"reserve_data":{"ssh_keys":["gh:x"]}}`)
	want := jobSpec{Queue: "q", ProvisionData: map[string]any{"distro": "noble", "disks": 2}, SSHKeys: []string{"gh:x"}}
	if !jobSpecMatches(show, want) {
		t.Fatal("int vs float64 must compare equal")
	}
}

func TestLooselyEqualNilMaps(t *testing.T) {
	var a, b map[string]any
	if !looselyEqual(a, b) {
		t.Fatal("two nil maps should be equal")
	}
	if looselyEqual(map[string]any{"k": 1}, b) {
		t.Fatal("populated vs nil map should differ")
	}
}

func TestSpecFromJobDoc(t *testing.T) {
	doc, err := prepareJobDoc(nil, config{
		JobQueue:      "megatron",
		ProvisionData: map[string]any{"distro": "resolute"},
		SSHKeys:       []string{"gh:talhaHavadar"},
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := specFromJobDoc(doc)
	if spec.Queue != "megatron" {
		t.Fatalf("queue = %q", spec.Queue)
	}
	if spec.ProvisionData["distro"] != "resolute" {
		t.Fatalf("provision data = %v", spec.ProvisionData)
	}
	if len(spec.SSHKeys) != 1 || spec.SSHKeys[0] != "gh:talhaHavadar" {
		t.Fatalf("ssh keys = %v", spec.SSHKeys)
	}
}

func TestTailString(t *testing.T) {
	if got := tailString("short", 100); got != "short" {
		t.Fatalf("short input mangled: %q", got)
	}
	long := "line1\nline2\nline3"
	got := tailString(long, 12)
	if got != "line2\nline3" {
		t.Fatalf("tail = %q", got)
	}
	// No newline inside the window: raw byte tail.
	if got := tailString("abcdefghij", 4); got != "ghij" {
		t.Fatalf("raw tail = %q", got)
	}
}

func TestParseConfigNewFields(t *testing.T) {
	c, err := parseConfig([]byte(`{"job_queue":"q"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !c.keepReservation() {
		t.Fatal("keep_reservation must default to true")
	}
	if c.AdoptMinRemainingSecs != defaultAdoptMinRemaining || c.WatchIntervalSecs != defaultWatchInterval {
		t.Fatalf("defaults = %d/%d", c.AdoptMinRemainingSecs, c.WatchIntervalSecs)
	}

	c, err = parseConfig([]byte(`{"job_queue":"q","keep_reservation":false,"wait_if_busy":true,"ssh_user":"root"}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.keepReservation() {
		t.Fatal("explicit keep_reservation:false ignored")
	}
	if !c.WaitIfBusy || c.SSHUser != "root" {
		t.Fatalf("config = %+v", c)
	}

	if _, err := parseConfig([]byte(`{"job_queue":"q","ssh_info_regex":"("}`)); err == nil {
		t.Fatal("invalid ssh_info_regex must be rejected at config parse")
	}
}

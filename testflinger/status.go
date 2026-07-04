package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Job states reported by `testflinger status`. A reserve job walks the
// in-flight states, lands on reserve, then ends via cleanup/complete —
// cancelled jobs have been observed to end as "complete" too, but the state
// is kept in the terminal set defensively.
var (
	terminalStates = map[string]bool{"complete": true, "cancelled": true, "cleanup": true}
	inFlightStates = map[string]bool{"waiting": true, "setup": true, "provision": true,
		"firmware_update": true, "test": true, "allocate": true}
	// pollPhases are the values `poll --phase` accepts; "waiting" is a job
	// state but not a phase with a log.
	pollPhases = map[string]bool{"setup": true, "provision": true, "firmware_update": true,
		"test": true, "allocate": true, "reserve": true, "cleanup": true}
)

// phaseOrder is the order phases run in, so the first failure reported is the
// root cause rather than a later cascade.
var phaseOrder = []string{"setup", "provision", "firmware_update", "test", "allocate", "reserve", "cleanup"}

// queueStatus is the parsed `queue-status --json` response.
type queueStatus struct {
	Queue       string
	Agents      []queueAgent
	JobsWaiting []string
}

type queueAgent struct {
	Name   string
	Status string
}

// parseQueueStatus parses `queue-status --json`. jobs_waiting has been
// observed empty; tolerate both plain job-id strings and objects carrying a
// job_id — the count is what pre-flight decisions need.
func parseQueueStatus(data []byte) (queueStatus, error) {
	var raw struct {
		Queue  string `json:"queue"`
		Agents []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"agents"`
		JobsWaiting []any `json:"jobs_waiting"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return queueStatus{}, fmt.Errorf("parsing queue-status json: %w", err)
	}
	qs := queueStatus{Queue: raw.Queue}
	for _, a := range raw.Agents {
		qs.Agents = append(qs.Agents, queueAgent{Name: a.Name, Status: a.Status})
	}
	for _, j := range raw.JobsWaiting {
		switch v := j.(type) {
		case string:
			qs.JobsWaiting = append(qs.JobsWaiting, v)
		case map[string]any:
			id, _ := v["job_id"].(string)
			qs.JobsWaiting = append(qs.JobsWaiting, id)
		default:
			qs.JobsWaiting = append(qs.JobsWaiting, "")
		}
	}
	return qs, nil
}

// queueVerdict is the pre-flight decision for a queue.
type queueVerdict struct {
	Proceed bool
	Reason  string
}

// classifyQueue decides whether submitting to the queue makes sense right
// now. ourJobs marks job ids we submitted ourselves, so our own queued work
// does not count as competition. waitIfBusy turns the busy-with-a-backlog
// case from a refusal into a queued submit.
func classifyQueue(qs queueStatus, ourJobs map[string]bool, waitIfBusy bool) queueVerdict {
	if len(qs.Agents) == 0 {
		return queueVerdict{false, "queue has no agents"}
	}
	var free, out int
	var busyStates []string
	for _, a := range qs.Agents {
		switch a.Status {
		case "waiting":
			free++
		case "offline", "maintenance":
			out++
		default:
			busyStates = append(busyStates, a.Name+":"+a.Status)
		}
	}
	waitingOthers := 0
	for _, id := range qs.JobsWaiting {
		if !ourJobs[id] {
			waitingOthers++
		}
	}
	summary := fmt.Sprintf("%d agent(s): %d free, %d busy, %d offline/maintenance; %d job(s) waiting",
		len(qs.Agents), free, len(busyStates), out, len(qs.JobsWaiting))
	switch {
	case free > 0:
		return queueVerdict{true, summary}
	case len(busyStates) == 0:
		return queueVerdict{false, "all agents offline or in maintenance (" + summary + ")"}
	case waitingOthers == 0:
		return queueVerdict{true, "agents busy (" + strings.Join(busyStates, ", ") +
			") but nothing queued ahead of us (" + summary + ")"}
	case waitIfBusy:
		return queueVerdict{true, fmt.Sprintf("%d job(s) queued ahead; queuing anyway per wait_if_busy (%s)",
			waitingOthers, summary)}
	default:
		return queueVerdict{false, fmt.Sprintf("agents busy (%s) with %d job(s) already waiting; set wait_if_busy to queue behind them (%s)",
			strings.Join(busyStates, ", "), waitingOthers, summary)}
	}
}

// jobRow is one line of `testflinger jobs` history (chronological).
type jobRow struct {
	ID    string
	Queue string
}

// parseJobsList parses the columnar `jobs` output: a header, a dashed rule,
// then "<job id>  <submission time>  <queue>" rows.
func parseJobsList(out string) []jobRow {
	var rows []jobRow
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || jobIDRE.FindString(fields[0]) != fields[0] {
			continue
		}
		rows = append(rows, jobRow{ID: fields[0], Queue: fields[len(fields)-1]})
	}
	return rows
}

// jobResults is the parsed `results <job>` JSON.
type jobResults struct {
	JobState string
	DeviceIP string
	Phases   map[string]phaseResult
}

// phaseResult is one phase's exit status and captured output.
type phaseResult struct {
	Status int
	Output string
}

// parseResults extracts what we need from the results JSON: the structured
// device address (device_info is populated as soon as the machine is
// provisioned, not only on completion), the job state, and per-phase exit
// statuses for failure reporting.
func parseResults(data []byte) (jobResults, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return jobResults{}, fmt.Errorf("parsing results json: %w", err)
	}
	jr := jobResults{Phases: map[string]phaseResult{}}
	jr.JobState, _ = raw["job_state"].(string)
	if di, ok := raw["device_info"].(map[string]any); ok {
		jr.DeviceIP, _ = di["device_ip"].(string)
	}
	for k, v := range raw {
		if name, found := strings.CutSuffix(k, "_status"); found {
			if f, ok := v.(float64); ok {
				pr := jr.Phases[name]
				pr.Status = int(f)
				jr.Phases[name] = pr
			}
		}
		if name, found := strings.CutSuffix(k, "_output"); found {
			if s, ok := v.(string); ok {
				pr := jr.Phases[name]
				pr.Output = s
				jr.Phases[name] = pr
			}
		}
	}
	return jr, nil
}

// failedPhase returns the earliest phase whose exit status is non-zero.
func failedPhase(jr jobResults) (string, phaseResult, bool) {
	for _, name := range phaseOrder {
		if pr, ok := jr.Phases[name]; ok && pr.Status != 0 {
			return name, pr, true
		}
	}
	return "", phaseResult{}, false
}

var reserveExpiryRE = regexp.MustCompile(`Reservation expires at:\s*\[([^\]]+)\]`)

// parseReserveExpiry pulls the reservation's absolute expiry out of the
// reserve-phase log ("Reservation expires at: [2026-07-05T03:12:04.969+00:00]").
func parseReserveExpiry(log string) (time.Time, bool) {
	m := lastSubmatch(reserveExpiryRE, log)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(m[1]))
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// jobSpecMatches reports whether a previously submitted job (its `show` JSON)
// describes the reservation we want: same queue, same provision_data, and
// every ssh key we need already authorized on it (a superset is fine — we
// can still log in).
func jobSpecMatches(showJSON []byte, want jobSpec) bool {
	var job struct {
		JobQueue      string         `json:"job_queue"`
		ProvisionData map[string]any `json:"provision_data"`
		ReserveData   struct {
			SSHKeys []any `json:"ssh_keys"`
		} `json:"reserve_data"`
	}
	if err := json.Unmarshal(showJSON, &job); err != nil {
		return false
	}
	if job.JobQueue != want.Queue {
		return false
	}
	if !looselyEqual(job.ProvisionData, want.ProvisionData) {
		return false
	}
	have := map[string]bool{}
	for _, k := range job.ReserveData.SSHKeys {
		if s, ok := k.(string); ok {
			have[s] = true
		}
	}
	for _, k := range want.SSHKeys {
		if !have[k] {
			return false
		}
	}
	return true
}

// looselyEqual compares two decoded documents structurally, treating all
// numbers as float64 — YAML decodes 6 as int where JSON gives float64, and
// that difference must not defeat a spec match.
func looselyEqual(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			bvv, ok := bv[k]
			if !ok || !looselyEqual(v, bvv) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !looselyEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		if af, ok := toFloat(a); ok {
			bf, ok := toFloat(b)
			return ok && af == bf
		}
		return a == b
	}
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// tailString returns at most max bytes from the end of s, starting at a line
// boundary when one is available.
func tailString(s string, max int) string {
	s = strings.TrimRight(s, "\n")
	if len(s) <= max {
		return s
	}
	s = s[len(s)-max:]
	if i := strings.IndexByte(s, '\n'); i >= 0 && i+1 < len(s) {
		s = s[i+1:]
	}
	return s
}

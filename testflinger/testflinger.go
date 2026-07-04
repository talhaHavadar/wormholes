package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

const (
	defaultTestflingerBin    = "testflinger-cli"
	defaultReserveTimeout    = 21600 // 6h, the unauthenticated maximum
	defaultPollTimeout       = 120   // seconds any single testflinger-cli call may take
	defaultPollInterval      = 20    // seconds between wait-loop status checks
	defaultAdoptMinRemaining = 900   // seconds a reservation must have left to be worth adopting
	defaultWatchInterval     = 60    // seconds between live job-state checks
	defaultSSHUser           = "ubuntu"

	cancelTimeoutSecs = 30   // bound on the best-effort `cancel` at teardown
	adoptScanLimit    = 5    // newest same-queue history entries examined for adoption
	failTailBytes     = 2048 // how much failing-phase output to include in errors

	// expirySafetyMargin ends the link slightly before the reservation's
	// stated expiry so we never hand a command to a machine mid-reclaim.
	expirySafetyMargin = 30 * time.Second
)

// sshUnreachableRE matches stderr fragments that OpenSSH emits when it could
// not reach the peer at all, as opposed to a real remote command failing.
// Used to distinguish "the reservation is gone" from "my build script exited
// 1", so we only signal the link dead in the former case.
var sshUnreachableRE = regexp.MustCompile(
	`(?i)connection refused|no route to host|connection timed out|host is down|network is unreachable|connection reset by peer|ssh_exchange_identification: connection closed|kex_exchange_identification|port 22: (?:connection|no route)|permission denied \(publickey`,
)

// config is the admin-supplied link configuration.
type config struct {
	// JobFile is an optional path, on the orchestrator, to a base Testflinger
	// reserve job YAML. It is the fallback: the direct fields below override
	// anything it sets, so a job can be described entirely in config without a
	// file.
	JobFile string `json:"job_file"`
	// JobQueue is the Testflinger queue to submit to. Overrides the job file's
	// job_queue; required if no job file supplies one.
	JobQueue string `json:"job_queue"`
	// ProvisionData is the Testflinger provision_data block (e.g. {distro: noble}).
	// Its keys are merged over the job file's provision_data, taking priority.
	ProvisionData map[string]any `json:"provision_data"`
	// SSHKeys are identities (gh:user / lp:user) injected into reserve_data.
	SSHKeys []string `json:"ssh_keys"`
	// ReserveTimeoutSecs is the reservation duration and the upper bound on how
	// long we wait for the machine to come up.
	ReserveTimeoutSecs int `json:"reserve_timeout_secs"`
	// Server overrides the Testflinger server URL (else TESTFLINGER_SERVER).
	Server string `json:"server"`
	// TestflingerBin is the CLI to invoke on the orchestrator.
	TestflingerBin string `json:"testflinger_bin"`
	// SSHInfoRegex overrides how the reserved host is parsed from the reserve
	// log. It must capture (user, host) in groups 1 and 2; when set it takes
	// priority over the structured results device_info.
	SSHInfoRegex string `json:"ssh_info_regex"`
	// SSHOptions are extra options passed to the ssh client on the orchestrator.
	SSHOptions []string `json:"ssh_options"`
	// PollTimeoutSecs bounds any single testflinger-cli invocation;
	// PollIntervalSecs is the gap between wait-loop status checks.
	PollTimeoutSecs  int `json:"poll_timeout_secs"`
	PollIntervalSecs int `json:"poll_interval_secs"`
	// KeepReservation makes reservations outlive links (default true): opening
	// a link first tries to adopt a matching live or in-flight job from our
	// own history, and closing the link leaves the reservation running for the
	// next link to adopt. Set false for submit-fresh/cancel-on-close.
	KeepReservation *bool `json:"keep_reservation"`
	// WaitIfBusy submits even when the queue's agents are all busy and other
	// jobs are already waiting ahead of us. The default is to fail fast with
	// the queue picture so the operator can decide.
	WaitIfBusy bool `json:"wait_if_busy"`
	// SSHUser overrides the login user on the reserved machine (default:
	// parsed from the reserve log, falling back to "ubuntu").
	SSHUser string `json:"ssh_user"`
	// AdoptMinRemainingSecs is the minimum remaining reservation time for an
	// existing reservation to be worth adopting.
	AdoptMinRemainingSecs int `json:"adopt_min_remaining_secs"`
	// WatchIntervalSecs is how often the live link re-checks the job state to
	// notice external cancellation.
	WatchIntervalSecs int `json:"watch_interval_secs"`
}

func parseConfig(raw json.RawMessage) (config, error) {
	c := config{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &c); err != nil {
			return config{}, fmt.Errorf("testflinger config: %w", err)
		}
	}
	if c.JobFile == "" && c.JobQueue == "" {
		return config{}, fmt.Errorf("testflinger config: set job_queue (with provision_data/ssh_keys) directly, or point job_file at a base job")
	}
	if c.SSHInfoRegex != "" {
		if _, err := regexp.Compile(c.SSHInfoRegex); err != nil {
			return config{}, fmt.Errorf("testflinger config: invalid ssh_info_regex: %w", err)
		}
	}
	if c.TestflingerBin == "" {
		c.TestflingerBin = defaultTestflingerBin
	}
	if c.ReserveTimeoutSecs <= 0 {
		c.ReserveTimeoutSecs = defaultReserveTimeout
	}
	if c.PollTimeoutSecs <= 0 {
		c.PollTimeoutSecs = defaultPollTimeout
	}
	if c.PollIntervalSecs <= 0 {
		c.PollIntervalSecs = defaultPollInterval
	}
	if c.AdoptMinRemainingSecs <= 0 {
		c.AdoptMinRemainingSecs = defaultAdoptMinRemaining
	}
	if c.WatchIntervalSecs <= 0 {
		c.WatchIntervalSecs = defaultWatchInterval
	}
	return c, nil
}

// keepReservation reports whether reservations outlive links (the default).
func (c config) keepReservation() bool { return c.KeepReservation == nil || *c.KeepReservation }

// reservation is a live reserved machine we can SSH into.
type reservation struct {
	JobID  string
	Target sshTarget
	// Expiry is when the reservation ends (parsed from the reserve log, or
	// approximated from config when the log gives nothing).
	Expiry time.Time
	// Adopted marks reservations we latched onto rather than submitted.
	Adopted bool
}

// openLink acquires a reserved machine (adopting an existing reservation when
// possible, else pre-flighting the queue and submitting) and provides an
// exec-endpoint that runs commands on it over SSH from the orchestrator.
func openLink(ctx context.Context, req *wormhole.LinkRequest) (*wormhole.ActiveLink, error) {
	cfg, err := parseConfig(req.Config)
	if err != nil {
		return nil, err
	}

	link, ok := findLink(req.Links, wormhole.PortTypeExecEndpoint)
	if !ok {
		return nil, fmt.Errorf("testflinger requires an orchestrator exec-endpoint (the host running testflinger-cli)")
	}
	var ep wormhole.ExecEndpointDescriptor
	if err := link.DecodeDescriptor(&ep); err != nil {
		return nil, fmt.Errorf("decoding orchestrator endpoint: %w", err)
	}
	orch, err := wormhole.DialExecEndpoint(ep)
	if err != nil {
		return nil, fmt.Errorf("dialing orchestrator: %w", err)
	}

	res, err := acquire(ctx, req, cfg, orch)
	if err != nil {
		_ = orch.Close()
		return nil, err
	}

	// The reservation, not the link, owns the machine: once it expires or the
	// job leaves the reserve state, the machine goes back to the pool and its
	// IP may serve someone else's job. Died fires on whichever comes first:
	// the reservation's expiry deadline, the job-state watcher noticing the
	// job end early (external cancel, hardware fault), or the per-command SSH
	// unreachable heuristic below (which covers the watcher's blind window).
	died := make(chan struct{})
	var diedOnce sync.Once
	signalDied := func(reason string) {
		diedOnce.Do(func() {
			req.Logf("warn", "closing testflinger link: %s", reason)
			close(died)
		})
	}

	watchDone := make(chan struct{})
	go watchReservation(cfg, orch, res, signalDied, watchDone)

	run := func(ctx context.Context, cmd wormhole.Command, sink wormhole.ExecSink) error {
		w := &sshFailWatcher{sink: sink}
		err := runOverSSH(ctx, orch, cfg, res.Target, cmd, w)
		if w.sshUnreachable() {
			signalDied("ssh to reserved machine unreachable — reservation likely gone")
		}
		return err
	}
	desc, stop, err := wormhole.ServeExecEndpoint(wormhole.LinkSocketDir(req.LinkID), run)
	if err != nil {
		close(watchDone)
		abandonJob(cfg, orch, res.JobID)
		_ = orch.Close()
		return nil, err
	}
	return &wormhole.ActiveLink{
		Descriptor: desc,
		Died:       died,
		Close: func() error {
			close(watchDone)
			_ = stop()
			abandonJob(cfg, orch, res.JobID)
			return orch.Close()
		},
	}, nil
}

// acquire produces a live reservation: by adopting a matching job we already
// have on the queue (the default), or by pre-flighting the queue and
// submitting a fresh reserve job.
func acquire(ctx context.Context, req *wormhole.LinkRequest, cfg config, orch *wormhole.ExecRunner) (reservation, error) {
	var base []byte
	if cfg.JobFile != "" {
		req.Progress(-1, "reading testflinger job file")
		cat, err := capture(ctx, orch, wormhole.Command{Argv: []string{"cat", cfg.JobFile}})
		if err != nil || cat.exit != 0 {
			return reservation{}, fmt.Errorf("reading job file %s: %v %s", cfg.JobFile, err, strings.TrimSpace(cat.stderr))
		}
		base = []byte(cat.stdout)
	}
	doc, err := prepareJobDoc(base, cfg)
	if err != nil {
		return reservation{}, err
	}
	want := specFromJobDoc(doc)

	var history []jobRow
	if cfg.keepReservation() {
		req.Progress(-1, "looking for an adoptable reservation on "+want.Queue)
		history = jobHistory(ctx, cfg, orch)
		if cand, ok := findAdoptable(ctx, req, cfg, orch, history, want); ok {
			if cand.State == "reserve" {
				req.Logf("info", "adopting live reservation %s on %s (expires %s)",
					cand.JobID, want.Queue, cand.Expiry.Format(time.RFC3339))
				r, err := handoff(ctx, req, cfg, orch, cand.JobID, true)
				if err == nil {
					return r, nil
				}
				req.Logf("warn", "adopting %s failed (%v); falling back to a fresh job", cand.JobID, err)
			} else {
				req.Logf("info", "waiting on our in-flight job %s (state %s) instead of submitting a duplicate",
					cand.JobID, cand.State)
				return waitForReserve(ctx, req, cfg, orch, cand.JobID, true)
			}
		}
	}

	ours := make(map[string]bool, len(history))
	for _, r := range history {
		ours[r.ID] = true
	}
	if err := preflight(ctx, req, cfg, orch, want.Queue, ours); err != nil {
		return reservation{}, err
	}

	job, err := marshalJob(doc)
	if err != nil {
		return reservation{}, err
	}

	// Pipe the prepared job straight into `testflinger-cli submit -` so we
	// never touch the orchestrator's filesystem. Avoids the snap-private /tmp/
	// problem when testflinger-cli is a strict-confined snap.
	req.Progress(-1, "submitting testflinger job")
	submitCmd := tfCmd(cfg, "submit", "-")
	submitCmd.Stdin = job
	sub, err := capture(ctx, orch, submitCmd)
	if err != nil || sub.exit != 0 {
		return reservation{}, fmt.Errorf("testflinger submit: %v %s", err, strings.TrimSpace(sub.stderr+sub.stdout))
	}
	jobID, err := parseJobID(sub.stdout + "\n" + sub.stderr)
	if err != nil {
		return reservation{}, err
	}
	req.Logf("info", "testflinger job %s submitted; waiting for the reserved machine (provisioning can take ~45 min)", jobID)

	return waitForReserve(ctx, req, cfg, orch, jobID, false)
}

// preflight checks the queue before submitting, so a bad queue name or a
// hopeless queue fails in seconds instead of after the full reserve timeout.
// Anything that looks like the CLI or server not supporting queue-status
// degrades to a warning — pre-flight must never block a submit that would
// have worked.
func preflight(ctx context.Context, req *wormhole.LinkRequest, cfg config, orch *wormhole.ExecRunner, queue string, ourJobs map[string]bool) error {
	req.Progress(-1, "checking queue "+queue)
	res, err := capture(ctx, orch, withTimeout(tfCmd(cfg, "queue-status", "--json", queue), cfg.PollTimeoutSecs))
	if err != nil {
		req.Logf("warn", "queue pre-flight skipped: %v", err)
		return nil
	}
	if res.exit != 0 {
		out := strings.TrimSpace(res.stdout + res.stderr)
		// The server's own verdict is terminal; an old CLI without
		// queue-status just means we submit blind like before.
		if strings.Contains(out, "does not exist") {
			return fmt.Errorf("testflinger queue %q: %s", queue, lastLine(out))
		}
		req.Logf("warn", "queue pre-flight skipped (queue-status unavailable): %s", lastLine(out))
		return nil
	}
	qs, err := parseQueueStatus([]byte(res.stdout))
	if err != nil {
		req.Logf("warn", "queue pre-flight skipped: %v", err)
		return nil
	}
	v := classifyQueue(qs, ourJobs, cfg.WaitIfBusy)
	if !v.Proceed {
		return fmt.Errorf("queue %q not ready: %s", queue, v.Reason)
	}
	req.Logf("info", "queue %s: %s", queue, v.Reason)
	return nil
}

// candidate is a previously submitted job considered for adoption.
type candidate struct {
	JobID  string
	State  string
	Expiry time.Time // set when State == "reserve"
}

// jobHistory lists the jobs previously submitted from this orchestrator (the
// CLI keeps the history client-side, so these are all ours). Best-effort:
// empty on any failure.
func jobHistory(ctx context.Context, cfg config, orch *wormhole.ExecRunner) []jobRow {
	res, err := capture(ctx, orch, withTimeout(tfCmd(cfg, "jobs"), cfg.PollTimeoutSecs))
	if err != nil || res.exit != 0 {
		return nil
	}
	return parseJobsList(res.stdout)
}

// findAdoptable scans our newest same-queue jobs for one worth latching onto:
// a live reservation with a matching spec and enough time left, or a matching
// in-flight job still on its way to reserve. Best-effort — any per-candidate
// error just skips that candidate.
func findAdoptable(ctx context.Context, req *wormhole.LinkRequest, cfg config, orch *wormhole.ExecRunner, history []jobRow, want jobSpec) (candidate, bool) {
	checked := 0
	for i := len(history) - 1; i >= 0 && checked < adoptScanLimit; i-- {
		row := history[i]
		if row.Queue != want.Queue {
			continue
		}
		checked++
		st, err := jobStatus(ctx, cfg, orch, row.ID)
		if err != nil || terminalStates[st] {
			continue
		}
		if st != "reserve" && !inFlightStates[st] {
			continue
		}
		show, err := capture(ctx, orch, withTimeout(tfCmd(cfg, "show", row.ID), cfg.PollTimeoutSecs))
		if err != nil || show.exit != 0 || !jobSpecMatches([]byte(show.stdout), want) {
			continue
		}
		if st != "reserve" {
			return candidate{JobID: row.ID, State: st}, true
		}
		exp, ok := parseReserveExpiry(phaseLog(ctx, cfg, orch, row.ID, "reserve"))
		if !ok {
			continue // can't tell how long it has left; not worth the risk
		}
		if remaining := time.Until(exp); remaining < time.Duration(cfg.AdoptMinRemainingSecs)*time.Second {
			req.Logf("info", "reservation %s matches but only has %s left (< adopt_min_remaining_secs); skipping",
				row.ID, remaining.Round(time.Second))
			continue
		}
		return candidate{JobID: row.ID, State: st, Expiry: exp}, true
	}
	return candidate{}, false
}

// waitForReserve polls the job's one-word state until it reaches reserve,
// failing fast the moment the job ends without reserving. adopted jobs were
// found, not submitted — like all jobs under keep_reservation they are never
// cancelled on the way out.
func waitForReserve(ctx context.Context, req *wormhole.LinkRequest, cfg config, orch *wormhole.ExecRunner, jobID string, adopted bool) (reservation, error) {
	deadline := time.Now().Add(time.Duration(cfg.ReserveTimeoutSecs) * time.Second)
	lastState := ""
	for {
		if err := ctx.Err(); err != nil {
			abandonJob(cfg, orch, jobID)
			return reservation{}, err
		}
		st, err := jobStatus(ctx, cfg, orch, jobID)
		switch {
		case err != nil:
			// Transient orchestrator/server hiccups just cost one interval;
			// the deadline still bounds the loop.
			req.Progress(-1, "waiting: status check failed: "+err.Error())
		case st == "reserve":
			return handoff(ctx, req, cfg, orch, jobID, adopted)
		case terminalStates[st]:
			return reservation{}, reserveFailure(ctx, cfg, orch, jobID, st)
		default:
			if st != lastState {
				req.Logf("info", "testflinger job %s: %s", jobID, st)
				lastState = st
			}
			msg := "waiting (" + st + ")"
			if pollPhases[st] {
				if line := lastLine(phaseLog(ctx, cfg, orch, jobID, st)); line != "" {
					msg += ": " + line
				}
			}
			req.Progress(-1, msg)
		}
		if time.Now().After(deadline) {
			abandonJob(cfg, orch, jobID)
			return reservation{}, fmt.Errorf("timed out after %ds waiting for testflinger reservation (job %s)", cfg.ReserveTimeoutSecs, jobID)
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(cfg.PollIntervalSecs) * time.Second):
		}
	}
}

// reserveFailure explains why the job ended without handing us a machine,
// naming the failing phase when the results JSON identifies one.
func reserveFailure(ctx context.Context, cfg config, orch *wormhole.ExecRunner, jobID, state string) error {
	res, err := capture(ctx, orch, withTimeout(tfCmd(cfg, "results", jobID), cfg.PollTimeoutSecs))
	if err == nil && res.exit == 0 {
		if jr, perr := parseResults([]byte(res.stdout)); perr == nil {
			if name, pr, ok := failedPhase(jr); ok {
				return fmt.Errorf("testflinger job %s ended (%s): %s phase failed with exit %d:\n%s",
					jobID, state, name, pr.Status, tailString(pr.Output, failTailBytes))
			}
		}
	}
	return fmt.Errorf("testflinger job %s ended (state %s) before the reservation was handed off", jobID, state)
}

// handoff resolves a job in reserve state to its SSH coordinates and expiry.
// The machine address comes from the structured results device_info; the
// login user and expiry from the reserve-phase log. parseSSHInfo — with the
// admin's ssh_info_regex when set — remains the fallback for both.
func handoff(ctx context.Context, req *wormhole.LinkRequest, cfg config, orch *wormhole.ExecRunner, jobID string, adopted bool) (reservation, error) {
	var customRE *regexp.Regexp
	if cfg.SSHInfoRegex != "" {
		re, err := regexp.Compile(cfg.SSHInfoRegex)
		if err != nil {
			return reservation{}, fmt.Errorf("invalid ssh_info_regex: %w", err)
		}
		customRE = re
	}

	rlog := phaseLog(ctx, cfg, orch, jobID, "reserve")

	var user, host string
	if t, ok := parseSSHInfo(rlog, customRE); ok {
		user, host = t.User, t.Host
	}
	if customRE == nil {
		if res, err := capture(ctx, orch, withTimeout(tfCmd(cfg, "results", jobID), cfg.PollTimeoutSecs)); err == nil && res.exit == 0 {
			if jr, perr := parseResults([]byte(res.stdout)); perr == nil && jr.DeviceIP != "" {
				host = jr.DeviceIP
			}
		}
	}
	if cfg.SSHUser != "" {
		user = cfg.SSHUser
	}
	if user == "" {
		user = defaultSSHUser
	}
	if host == "" {
		return reservation{}, fmt.Errorf("job %s is reserved but no machine address found (results device_info empty, nothing matched in the reserve log)", jobID)
	}

	expiry, ok := parseReserveExpiry(rlog)
	if !ok {
		if adopted {
			return reservation{}, fmt.Errorf("job %s: cannot determine reservation expiry from the reserve log", jobID)
		}
		expiry = time.Now().Add(time.Duration(cfg.ReserveTimeoutSecs) * time.Second)
	}

	req.Progress(1, "reserved "+user+"@"+host)
	req.Logf("info", "reserved machine ready: %s@%s (job %s, expires %s)", user, host, jobID, expiry.Format(time.RFC3339))
	return reservation{JobID: jobID, Target: sshTarget{User: user, Host: host}, Expiry: expiry, Adopted: adopted}, nil
}

// watchReservation ends the link when the reservation does: at the expiry
// deadline, or as soon as a periodic job-state check sees the job leave the
// reserve state (cancelled elsewhere, hardware pulled, ...).
func watchReservation(cfg config, orch *wormhole.ExecRunner, r reservation, signalDied func(string), done <-chan struct{}) {
	wait := time.Until(r.Expiry) - expirySafetyMargin
	if wait < time.Second {
		wait = time.Second
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(time.Duration(cfg.WatchIntervalSecs) * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			signalDied("reservation deadline reached")
			return
		case <-tick.C:
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.PollTimeoutSecs)*time.Second)
			st, err := jobStatus(ctx, cfg, orch, r.JobID)
			cancel()
			// Errors are transient by assumption; the ssh heuristic and the
			// deadline cover a watcher that can never get an answer.
			if err == nil && st != "" && st != "reserve" {
				signalDied("job " + r.JobID + " left reserve state: " + st)
				return
			}
		case <-done:
			return
		}
	}
}

// jobStatus fetches the job's one-word state.
func jobStatus(ctx context.Context, cfg config, orch *wormhole.ExecRunner, jobID string) (string, error) {
	res, err := capture(ctx, orch, withTimeout(tfCmd(cfg, "status", jobID), cfg.PollTimeoutSecs))
	if err != nil {
		return "", err
	}
	if res.exit != 0 {
		return "", fmt.Errorf("testflinger status %s: %s", jobID, lastLine(strings.TrimSpace(res.stderr+res.stdout)))
	}
	return lastLine(strings.TrimSpace(res.stdout)), nil
}

// phaseLog fetches the job's log for one phase without streaming
// (best-effort: empty on any failure).
func phaseLog(ctx context.Context, cfg config, orch *wormhole.ExecRunner, jobID, phase string) string {
	res, err := capture(ctx, orch, withTimeout(tfCmd(cfg, "poll", "--oneshot", "--phase", phase, jobID), cfg.PollTimeoutSecs))
	if err != nil || res.exit != 0 {
		return ""
	}
	return res.stdout
}

// runOverSSH runs one command on the reserved machine, from the orchestrator,
// over SSH. The remote working directory and environment are reconstructed in
// a shell command string because ssh does not carry them.
//
// Uses RunStream so each stdout/stderr chunk from the orchestrator's exec
// stream is forwarded straight to sink as it arrives. Run's buffer-and-flush
// would coalesce the whole command's output into a single sink.Stdout call
// which, for any review/build that produces more than ~4 MiB, exceeds the
// downstream consumer's default gRPC MaxRecvMsgSize and fails its Recv with
// ResourceExhausted.
func runOverSSH(ctx context.Context, orch *wormhole.ExecRunner, cfg config, t sshTarget, cmd wormhole.Command, sink wormhole.ExecSink) error {
	argv := []string{"ssh", "-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes"}
	argv = append(argv, cfg.SSHOptions...)
	argv = append(argv, t.User+"@"+t.Host, remoteShellCommand(cmd.Dir, cmd.Env, cmd.Argv))

	return orch.RunStream(ctx, wormhole.Command{Argv: argv, Stdin: cmd.Stdin, TimeoutMs: cmd.TimeoutMs}, sink)
}

// abandonJob cancels a job we gave up on — unless reservations are meant to
// outlive links (keep_reservation), in which case whatever this link leaves
// behind is re-adopted by the next open.
func abandonJob(cfg config, orch *wormhole.ExecRunner, jobID string) {
	if cfg.keepReservation() {
		return
	}
	cancelJob(cfg, orch, jobID)
}

// cancelJob is a best-effort cancel, bounded so a wedged orchestrator cannot
// hang link teardown.
func cancelJob(cfg config, orch *wormhole.ExecRunner, jobID string) {
	if jobID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cancelTimeoutSecs*time.Second)
	defer cancel()
	_, _ = capture(ctx, orch, withTimeout(tfCmd(cfg, "cancel", jobID), cancelTimeoutSecs))
}

// sshFailWatcher wraps an ExecSink and keeps a bounded tail of stderr so run
// can decide, after the command finishes, whether the failure was ssh giving
// up on the peer (link dead) rather than the remote command exiting non-zero.
type sshFailWatcher struct {
	sink wormhole.ExecSink

	mu         sync.Mutex
	exit       int
	hasExit    bool
	stderrTail []byte
}

const sshFailStderrTailBytes = 4096

func (w *sshFailWatcher) Stdout(p []byte) { w.sink.Stdout(p) }
func (w *sshFailWatcher) Stderr(p []byte) {
	w.mu.Lock()
	// Keep only the last sshFailStderrTailBytes of stderr so a chatty command
	// can't blow memory. The connection-refused/no-route messages are always
	// near the end of stderr.
	buf := append(w.stderrTail, p...)
	if len(buf) > sshFailStderrTailBytes {
		buf = buf[len(buf)-sshFailStderrTailBytes:]
	}
	w.stderrTail = buf
	w.mu.Unlock()
	w.sink.Stderr(p)
}
func (w *sshFailWatcher) SetExit(code int) {
	w.mu.Lock()
	w.exit = code
	w.hasExit = true
	w.mu.Unlock()
	w.sink.SetExit(code)
}

// sshUnreachable reports whether the failure looks like ssh could not reach
// the peer at all (dead reservation), rather than the remote command exiting
// non-zero (real command failure). Exit 255 is ssh's own "something went
// wrong before/at connection setup" code; combined with an unreachable-host
// stderr fragment it is a strong signal the reservation is gone.
func (w *sshFailWatcher) sshUnreachable() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.hasExit || w.exit != 255 {
		return false
	}
	return sshUnreachableRE.Match(w.stderrTail)
}

// tfCmd builds a testflinger-cli invocation with the configured binary/server.
func tfCmd(cfg config, args ...string) wormhole.Command {
	argv := []string{cfg.TestflingerBin}
	if cfg.Server != "" {
		argv = append(argv, "--server", cfg.Server)
	}
	argv = append(argv, args...)
	return wormhole.Command{Argv: argv}
}

func withTimeout(cmd wormhole.Command, secs int) wormhole.Command {
	cmd.TimeoutMs = int64(secs) * 1000
	return cmd
}

type captured struct {
	stdout, stderr string
	exit           int
}

// capture runs cmd through the orchestrator runner and collects its output.
func capture(ctx context.Context, orch *wormhole.ExecRunner, cmd wormhole.Command) (captured, error) {
	res, err := orch.Run(ctx, cmd)
	c := captured{exit: -1}
	if res != nil {
		c.stdout = string(res.Stdout)
		c.stderr = string(res.Stderr)
		c.exit = res.ExitCode
	}
	return c, err
}

func findLink(links []wormhole.Link, portType string) (wormhole.Link, bool) {
	for _, l := range links {
		if l.Type == portType {
			return l, true
		}
	}
	return wormhole.Link{}, false
}

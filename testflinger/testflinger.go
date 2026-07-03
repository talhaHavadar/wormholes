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
	defaultTestflingerBin = "testflinger-cli"
	defaultReserveTimeout = 21600 // 6h, the unauthenticated maximum
	defaultPollTimeout    = 120   // seconds a single `poll` is allowed to stream
	defaultPollInterval   = 20    // seconds between poll attempts
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
	// SSHInfoRegex overrides how the reserved host is parsed from poll output.
	// It must capture (user, host) in groups 1 and 2.
	SSHInfoRegex string `json:"ssh_info_regex"`
	// SSHOptions are extra options passed to the ssh client on the orchestrator.
	SSHOptions []string `json:"ssh_options"`
	// PollTimeoutSecs bounds a single `poll` invocation; PollIntervalSecs is the
	// gap between attempts.
	PollTimeoutSecs  int `json:"poll_timeout_secs"`
	PollIntervalSecs int `json:"poll_interval_secs"`
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
	return c, nil
}

// openLink reserves a machine through the orchestrator and provides an
// exec-endpoint that runs commands on it over SSH.
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

	jobID, target, err := reserve(ctx, req, cfg, orch)
	if err != nil {
		_ = orch.Close()
		return nil, err
	}

	// The testflinger reservation itself expires after ReserveTimeoutSecs
	// (that is the value we just submitted as reserve_data.timeout, and it
	// is authoritative here: prepareJob always writes the config's value
	// over the job_file's, and parseConfig always defaults it, so config
	// and job cannot disagree by construction). Once the reservation
	// expires the machine goes back to the pool and its IP may serve
	// someone else's job. Two things then must not happen:
	//   1. subsequent commands SSHing to `target` — the machine is not
	//      ours anymore, so anything they do is undefined;
	//   2. this session-manager link staying "up" — reusing it would just
	//      queue up more of (1) on future tool calls.
	//
	// Died fires when either condition is detected: a deadline timer for
	// (2), running from the point testflinger handed us the machine (so it
	// fires no earlier than the reservation itself ends), and an SSH
	// unreachable heuristic on every command for (1) — which also covers
	// the reservation dying early, e.g. hardware fault or admin cancel.
	died := make(chan struct{})
	var diedOnce sync.Once
	signalDied := func(reason string) {
		diedOnce.Do(func() {
			req.Logf("warn", "closing testflinger link: %s", reason)
			close(died)
		})
	}

	watchDone := make(chan struct{})
	go func() {
		t := time.NewTimer(time.Duration(cfg.ReserveTimeoutSecs) * time.Second)
		defer t.Stop()
		select {
		case <-t.C:
			signalDied("reservation deadline reached")
		case <-watchDone:
		}
	}()

	run := func(ctx context.Context, cmd wormhole.Command, sink wormhole.ExecSink) error {
		w := &sshFailWatcher{sink: sink}
		err := runOverSSH(ctx, orch, cfg, target, cmd, w)
		if w.sshUnreachable() {
			signalDied("ssh to reserved machine unreachable — reservation likely gone")
		}
		return err
	}
	desc, stop, err := wormhole.ServeExecEndpoint(wormhole.LinkSocketDir(req.LinkID), run)
	if err != nil {
		close(watchDone)
		cancelJob(cfg, orch, jobID)
		_ = orch.Close()
		return nil, err
	}
	return &wormhole.ActiveLink{
		Descriptor: desc,
		Died:       died,
		Close: func() error {
			close(watchDone)
			_ = stop()
			cancelJob(cfg, orch, jobID)
			return orch.Close()
		},
	}, nil
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

// reserve submits the job and waits for the reserved machine's SSH details.
func reserve(ctx context.Context, req *wormhole.LinkRequest, cfg config, orch *wormhole.ExecRunner) (string, sshTarget, error) {
	var base []byte
	if cfg.JobFile != "" {
		req.Progress(-1, "reading testflinger job file")
		cat, err := capture(ctx, orch, wormhole.Command{Argv: []string{"cat", cfg.JobFile}})
		if err != nil || cat.exit != 0 {
			return "", sshTarget{}, fmt.Errorf("reading job file %s: %v %s", cfg.JobFile, err, strings.TrimSpace(cat.stderr))
		}
		base = []byte(cat.stdout)
	}
	job, err := prepareJob(base, cfg)
	if err != nil {
		return "", sshTarget{}, err
	}

	// Pipe the prepared job straight into `testflinger-cli submit -` so we
	// never touch the orchestrator's filesystem. Avoids the snap-private /tmp/
	// problem when testflinger-cli is a strict-confined snap.
	req.Progress(-1, "submitting testflinger job")
	submitCmd := tfCmd(cfg, "submit", "-")
	submitCmd.Stdin = job
	sub, err := capture(ctx, orch, submitCmd)
	if err != nil || sub.exit != 0 {
		return "", sshTarget{}, fmt.Errorf("testflinger submit: %v %s", err, strings.TrimSpace(sub.stderr+sub.stdout))
	}
	jobID, err := parseJobID(sub.stdout + "\n" + sub.stderr)
	if err != nil {
		return "", sshTarget{}, err
	}
	req.Logf("info", "testflinger job %s submitted; waiting for the reserved machine (can take ~45 min)", jobID)

	var customRE *regexp.Regexp
	if cfg.SSHInfoRegex != "" {
		customRE, err = regexp.Compile(cfg.SSHInfoRegex)
		if err != nil {
			return "", sshTarget{}, fmt.Errorf("invalid ssh_info_regex: %w", err)
		}
	}

	deadline := time.Now().Add(time.Duration(cfg.ReserveTimeoutSecs) * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			cancelJob(cfg, orch, jobID)
			return "", sshTarget{}, err
		}
		// A single bounded poll: it streams output for a window then is killed,
		// giving us a chunk to scan for the connection details.
		poll, _ := capture(ctx, orch, withTimeout(tfCmd(cfg, "poll", jobID), cfg.PollTimeoutSecs))
		out := poll.stdout + "\n" + poll.stderr
		if t, ok := parseSSHInfo(out, customRE); ok {
			req.Progress(1, "reserved "+t.User+"@"+t.Host)
			req.Logf("info", "reserved machine ready: %s@%s", t.User, t.Host)
			return jobID, t, nil
		}
		req.Progress(-1, "waiting for reservation: "+lastLine(out))
		if time.Now().After(deadline) {
			cancelJob(cfg, orch, jobID)
			return "", sshTarget{}, fmt.Errorf("timed out after %ds waiting for testflinger reservation", cfg.ReserveTimeoutSecs)
		}
		time.Sleep(time.Duration(cfg.PollIntervalSecs) * time.Second)
	}
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

func cancelJob(cfg config, orch *wormhole.ExecRunner, jobID string) {
	if jobID == "" {
		return
	}
	_, _ = capture(context.Background(), orch, tfCmd(cfg, "cancel", jobID))
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

type captureSink struct {
	stdout strings.Builder
	stderr strings.Builder
	exit   int
}

func (c *captureSink) Stdout(p []byte)  { c.stdout.Write(p) }
func (c *captureSink) Stderr(p []byte)  { c.stderr.Write(p) }
func (c *captureSink) SetExit(code int) { c.exit = code }

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

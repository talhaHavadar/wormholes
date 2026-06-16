package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

const (
	defaultTestflingerBin = "testflinger-cli"
	defaultReserveTimeout = 21600 // 6h, the unauthenticated maximum
	defaultPollTimeout    = 120   // seconds a single `poll` is allowed to stream
	defaultPollInterval   = 20    // seconds between poll attempts
)

// config is the admin-supplied link configuration.
type config struct {
	// JobFile is the path, on the orchestrator, to a base Testflinger job YAML
	// using MAAS provisioning (job_queue + provision_data.distro).
	JobFile string `json:"job_file"`
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
	if c.JobFile == "" {
		return config{}, fmt.Errorf("testflinger config: job_file is required")
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

// openLink reserves a MAAS machine through the orchestrator and provides an
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

	run := func(ctx context.Context, cmd wormhole.Command, sink wormhole.ExecSink) error {
		return runOverSSH(ctx, orch, cfg, target, cmd, sink)
	}
	desc, stop, err := wormhole.ServeExecEndpoint(wormhole.LinkSocketDir(req.LinkID), run)
	if err != nil {
		cancelJob(cfg, orch, jobID)
		_ = orch.Close()
		return nil, err
	}
	return &wormhole.ActiveLink{
		Descriptor: desc,
		Close: func() error {
			_ = stop()
			cancelJob(cfg, orch, jobID)
			return orch.Close()
		},
	}, nil
}

// reserve submits the job and waits for the reserved machine's SSH details.
func reserve(ctx context.Context, req *wormhole.LinkRequest, cfg config, orch *wormhole.ExecRunner) (string, sshTarget, error) {
	req.Progress(-1, "reading testflinger job file")
	cat, err := capture(ctx, orch, wormhole.Command{Argv: []string{"cat", cfg.JobFile}})
	if err != nil || cat.exit != 0 {
		return "", sshTarget{}, fmt.Errorf("reading job file %s: %v %s", cfg.JobFile, err, strings.TrimSpace(cat.stderr))
	}
	job, err := prepareJob([]byte(cat.stdout), cfg.SSHKeys, cfg.ReserveTimeoutSecs)
	if err != nil {
		return "", sshTarget{}, err
	}
	if !looksLikeMAAS(job) {
		req.Logf("warn", "job provision_data does not look like MAAS (no distro); only MAAS is supported")
	}

	// Stage the prepared job on the orchestrator and submit it.
	tmp := "/tmp/interstellar-tf-" + req.LinkID + ".yaml"
	stage, err := capture(ctx, orch, wormhole.Command{
		Argv:  []string{"sh", "-c", "cat > " + shellQuote(tmp)},
		Stdin: job,
	})
	if err != nil || stage.exit != 0 {
		return "", sshTarget{}, fmt.Errorf("staging job on orchestrator: %v %s", err, strings.TrimSpace(stage.stderr))
	}
	req.Progress(-1, "submitting testflinger job")
	sub, err := capture(ctx, orch, tfCmd(cfg, "submit", tmp))
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
// over SSH. The remote working directory and environment are reconstructed in a
// shell command string because ssh does not carry them.
func runOverSSH(ctx context.Context, orch *wormhole.ExecRunner, cfg config, t sshTarget, cmd wormhole.Command, sink wormhole.ExecSink) error {
	argv := []string{"ssh", "-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes"}
	argv = append(argv, cfg.SSHOptions...)
	argv = append(argv, t.User+"@"+t.Host, remoteShellCommand(cmd.Dir, cmd.Env, cmd.Argv))

	res, err := orch.Run(ctx, wormhole.Command{Argv: argv, Stdin: cmd.Stdin, TimeoutMs: cmd.TimeoutMs})
	if res != nil {
		if len(res.Stdout) > 0 {
			sink.Stdout(res.Stdout)
		}
		if len(res.Stderr) > 0 {
			sink.Stderr(res.Stderr)
		}
		sink.SetExit(res.ExitCode)
	}
	return err
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

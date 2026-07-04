// Command testflinger is a wormhole that provides an exec-endpoint into a
// baremetal machine reserved via Canonical Testflinger. It drives
// testflinger-cli on an orchestrator host (reached via its required
// exec-endpoint port, itself optionally tunnelled over ssh+tailscale), then
// runs each command on the reserved machine over SSH from the orchestrator.
//
// Opening a link is a small state machine rather than a blind submit:
//
//   - adopt: with keep_reservation (the default), a matching live reservation
//     from our own job history is adopted — link up in seconds instead of a
//     fresh ~45 min provision — and a matching in-flight job is waited on
//     instead of submitting a duplicate;
//   - pre-flight: before submitting, queue-status verifies the queue exists
//     and has a working agent; a queue whose agents are busy with other jobs
//     already waiting ahead is refused unless wait_if_busy is set;
//   - wait: the job's one-word status is polled until it reaches reserve; a
//     job that ends first fails immediately, naming the failing phase;
//   - hand-off: the machine address comes from the results device_info, the
//     login user and expiry from the reserve log. The link's deadline is the
//     reservation's actual expiry, and a background watcher ends the link
//     early if the job leaves the reserve state (e.g. cancelled elsewhere).
//
// The job can be described directly in config (job_queue + provision_data) or
// sourced from a base job_file; when both are given the direct fields win and
// the file is the fallback.
//
// It exposes no agent-facing tools, only the port. A consumer such as
// contained-debdev (or debian-packager directly) holds the link and decides
// what runs on the reserved machine.
//
// Target configuration (admin-supplied):
//
//	targets:
//	  tf-maas:
//	    wormhole: testflinger
//	    port: reserved
//	    config:
//	      job_queue: maas-x86                     # required (here or in job_file)
//	      provision_data: { distro: noble }       # merged over job_file
//	      ssh_keys: [gh:talhaHavadar]
//	      reserve_timeout_secs: 21600
//	      # job_file: ~/sandbox/testflinger.yaml  # optional base; direct fields override it
//	      # keep_reservation: true                # default: adopt matching jobs, don't cancel on close
//	      # wait_if_busy: false                   # default: refuse a queue with a backlog
//	      # ssh_user: ubuntu                      # default: parsed from the reserve log
//	      # adopt_min_remaining_secs: 900         # don't adopt reservations with less time left
//	      # watch_interval_secs: 60               # live job-state check cadence
//	    via:
//	      orchestrator: tf-box                    # ssh target with testflinger-cli
package main

import "github.com/talhaHavadar/interstellar/pkg/wormhole"

var version = "0.2.0"

func main() {
	w := wormhole.New("testflinger", version,
		"Provides an exec-endpoint into a baremetal machine reserved via Testflinger.")

	// Required: the host that runs testflinger-cli and can reach the lab.
	w.Require(wormhole.Port{
		Name:        "orchestrator",
		Type:        wormhole.PortTypeExecEndpoint,
		Description: "host that runs testflinger-cli (reached over ssh, optionally tunnelled)",
	})

	w.Provide(
		wormhole.Port{
			Name:        "reserved",
			Type:        wormhole.PortTypeExecEndpoint,
			Description: "command execution on the reserved baremetal machine over SSH",
		},
		openLink,
	)

	w.Serve()
}

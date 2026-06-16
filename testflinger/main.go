// Command testflinger is a wormhole that provides an exec-endpoint into a
// baremetal machine reserved via Canonical Testflinger (MAAS provisioning). It
// reserves a system through an orchestrator host that runs testflinger-cli
// (reached via its required exec-endpoint port, itself optionally tunnelled
// over ssh+tailscale), waits for the reservation, then runs each command on the
// reserved machine over SSH from the orchestrator.
//
// Only the MAAS provisioning method is supported: point job_file at a base
// Testflinger job whose provision_data targets a MAAS queue (a `distro`).
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
//	      job_file: ~/sandbox/testflinger.yaml   # MAAS job (job_queue + provision_data.distro)
//	      ssh_keys: [gh:talhaHavadar]
//	      reserve_timeout_secs: 21600
//	    via:
//	      orchestrator: tf-box                    # ssh target with testflinger-cli
package main

import "github.com/talhaHavadar/interstellar/pkg/wormhole"

var version = "0.1.0"

func main() {
	w := wormhole.New("testflinger", version,
		"Provides an exec-endpoint into a MAAS baremetal machine reserved via Testflinger.")

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

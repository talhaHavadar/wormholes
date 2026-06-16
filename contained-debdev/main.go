// Command contained-debdev is a wormhole that provides an exec-endpoint which
// runs each command inside a contained-debdev container (via the `contained`
// script). The container runs on the local gateway host, or — when the
// optional "host" exec-endpoint port is linked — on a remote machine (an ssh
// target, a testflinger-reserved baremetal, ...), so Debian builds happen in a
// reproducible, tool-complete environment wherever the admin points it.
//
// It exposes no agent-facing tools, only the port; a consumer such as
// debian-packager holds the link and decides which commands run.
//
// Target configuration (admin-supplied, never from the agent):
//
//	targets:
//	  local-unstable:
//	    wormhole: contained-debdev
//	    port: exec
//	    config:
//	      image: ghcr.io/talhahavadar/contained-debdev:debian-unstable
//	  reserved-trixie:
//	    wormhole: contained-debdev
//	    port: exec
//	    config:
//	      image: ghcr.io/talhahavadar/contained-debdev:debian-trixie
//	      ensure_deps: true       # write `contained` + verify podman on a clean box
//	      install_runtime: true   # apt-get install podman if absent
//	    via:
//	      host: tf-maas           # run the container on a reserved baremetal
package main

import (
	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

var version = "0.1.0"

func main() {
	w := wormhole.New("contained-debdev", version,
		"Runs Debian-packaging commands inside a contained-debdev container, locally or on a linked host.")

	// Optional: where the container runs. Unlinked → the local gateway host.
	w.Require(wormhole.Port{
		Name:        "host",
		Type:        wormhole.PortTypeExecEndpoint,
		Optional:    true,
		Description: "host to run the container on (ssh/testflinger target); the local host if absent",
	})

	w.Provide(
		wormhole.Port{
			Name:        "exec",
			Type:        wormhole.PortTypeExecEndpoint,
			Description: "command execution inside a contained-debdev container",
		},
		openLink,
	)

	w.Serve()
}

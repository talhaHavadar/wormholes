// Command debian-packager is a wormhole that builds and inspects Debian
// packages through typed, purpose-built operations. Each tool runs a fixed
// command sequence on a builder it reaches through a required exec-endpoint
// port — the admin decides whether that builder is a local container
// (contained-debdev), a remote host, or reserved baremetal; the packager is
// identical in every case and never runs a caller-supplied command.
//
// The agent chooses the builder per call via the core-injected
// `builder_target` argument.
//
// Target configuration (admin-supplied): bind the "builder" port to any
// exec-endpoint target, e.g. a contained-debdev container:
//
//	targets:
//	  local-unstable:
//	    wormhole: contained-debdev
//	    port: exec
//	    config: { image: ghcr.io/talhahavadar/contained-debdev:debian-unstable }
package main

import (
	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

var version = "dev" // overridden at link time via -ldflags "-X main.version=..."

func main() {
	w := wormhole.New("debian-packager", version,
		"Builds and inspects Debian packages (sbuild, source, lintian, uscan) on a linked builder.")

	w.Require(wormhole.Port{
		Name:        "builder",
		Type:        wormhole.PortTypeExecEndpoint,
		Description: "where build/inspect commands run (e.g. a contained-debdev container)",
	})

	wormhole.AddTool(w, wormhole.Tool[buildBinaryInput]{
		Name:          "build_binary_package",
		Description:   "Build Debian binary packages (.deb) from a source tree with sbuild.",
		Capabilities:  []wormhole.Capability{wormhole.CapExecScoped, wormhole.CapNetwork},
		RequiresPorts: []string{"builder"},
		Handler:       buildBinaryPackage,
	})
	wormhole.AddTool(w, wormhole.Tool[buildSourceInput]{
		Name:          "build_source_package",
		Description:   "Build a Debian source package (.dsc/.changes) for upload.",
		Capabilities:  []wormhole.Capability{wormhole.CapExecScoped, wormhole.CapNetwork},
		RequiresPorts: []string{"builder"},
		Handler:       buildSourcePackage,
	})
	wormhole.AddTool(w, wormhole.Tool[lintInput]{
		Name:          "lint",
		Description:   "Build a package from source and run lintian on it, or (with a ppa) pull and lint its prebuilt Launchpad artifacts.",
		Capabilities:  []wormhole.Capability{wormhole.CapExecScoped, wormhole.CapNetwork},
		RequiresPorts: []string{"builder"},
		Handler:       lint,
	})
	wormhole.AddTool(w, wormhole.Tool[checkWatchInput]{
		Name:          "check_watch",
		Description:   "Check debian/watch with uscan for a newer upstream release.",
		Capabilities:  []wormhole.Capability{wormhole.CapExecScoped, wormhole.CapNetwork},
		RequiresPorts: []string{"builder"},
		Handler:       checkWatch,
	})
	wormhole.AddTool(w, wormhole.Tool[reviewInput]{
		Name:          "review",
		Description:   "Run the Debian package review checklist on a source tree (watch, lintian, copyright, DEP-3 patch headers, debian/control hygiene, symbols, changelog, hardening, autopkgtest, upstream metadata, …) and return one report entry per step for the agent to evaluate.",
		Capabilities:  []wormhole.Capability{wormhole.CapExecScoped, wormhole.CapNetwork},
		RequiresPorts: []string{"builder"},
		Handler:       review,
	})

	w.Serve()
}

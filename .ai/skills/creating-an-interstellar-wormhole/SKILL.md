---
name: creating-an-interstellar-wormhole
description: >-
  Author an Interstellar wormhole — a Go gRPC plugin for the Interstellar MCP
  gateway that exposes typed, purpose-built tools and/or composable ports to AI
  agents. Use when creating a new wormhole, adding tools or exec/network/mcp
  endpoint ports, wiring composition via targets, streaming progress from a
  long operation, or packaging and shipping a wormhole. Covers the pkg/wormhole
  SDK, capabilities, links, and the build/CI conventions of the wormholes repo.
license: MIT
compatibility: Requires Go 1.26+ and the github.com/talhaHavadar/interstellar SDK.
metadata:
  author: talhaHavadar
  version: "0.1.0"
---

# Creating an Interstellar wormhole

Interstellar is an MCP gateway that sits between AI agents and infrastructure.
A **wormhole** is a single Go binary, built on the `pkg/wormhole` SDK, that the
gateway launches as a subprocess and talks to over gRPC. You never deal with
MCP, JSON-RPC, or gRPC directly — you declare tools and ports, and the core
mediates every call, enforces capability policy, and writes an audit log.

## The one rule: typed operations, not command runners

Build **purpose-built protocols, not command runners.** A good wormhole exposes
a handful of typed operations that encode a workflow — `build_source_package(distro)`,
not `run(cmd)` — and _chooses the commands itself_. If a tool's input is "a
command to run", you are building an `exec.arbitrary` tool: denied by default
policy and invisible to agents until an admin opts in. If you need to _reach_
somewhere to do the job, don't take a host/credentials as input — **declare a
required port** and let the admin's configuration decide what's on the other
side. See `references/composition.md`.

Always make sure to write wormholes compatible with wormhole SDK in interstellar,
which you can find in https://github.com/talhaHavadar/interstellar repository.

## Minimal wormhole

```go
package main

import "github.com/talhaHavadar/interstellar/pkg/wormhole"

type greetInput struct {
	Name string `json:"name" jsonschema:"who to greet"`
}

func main() {
	w := wormhole.New("greeter", "0.1.0", "Greets people, properly.")
	wormhole.AddTool(w, wormhole.Tool[greetInput]{
		Name:         "greet",
		Description:  "Produce a greeting.",
		Capabilities: []wormhole.Capability{wormhole.CapRead},
		Handler: func(ctx context.Context, call *wormhole.Call, in greetInput) (any, error) {
			return map[string]string{"greeting": "Hello, " + in.Name}, nil
		},
	})
	w.Serve()
}
```

That is a complete wormhole. The agent sees a tool named `greeter__greet`
(`<wormhole>__<tool>`).

- The **input struct IS the contract**: its JSON Schema is generated from the
  fields. `json` tags name them; `jsonschema` tags describe them. Only exported
  fields appear.
- The handler's return value is JSON-marshaled back to the agent. Returning an
  error produces a tool error; panics are caught and reported the same way.
- **Never write to stdout** — the plugin handshake owns it. Log with
  `call.Logf` or write to stderr.

## Naming & structure

- Wormhole name: lowercase **kebab-case** (`^[a-z][a-z0-9-]{0,63}$`).
- Tool name: lowercase **snake_case** (`^[a-z][a-z0-9_]{0,63}$`).
- One Go module per wormhole: `module github.com/<you>/wormholes/<name>`.
- Files: `main.go` (wiring), `<domain>.go` (logic), `<domain>_test.go`,
  `Dockerfile`. Version lives in the `wormhole.New(name, "x.y.z", desc)` call.

## Capabilities (declare honestly — the audit log sees everything)

| Constant           | Meaning                                                       |
| ------------------ | ------------------------------------------------------------- |
| `CapRead`          | reads state, no side effects                                  |
| `CapWrite`         | mutates state via a fixed procedure                           |
| `CapNetwork`       | establishes/alters connectivity                               |
| `CapExecScoped`    | runs commands _the wormhole chooses_ (e.g. "build a package") |
| `CapExecArbitrary` | runs commands _the caller supplies_ — denied by default       |

Every tool declares at least one. They are validated at load and enforced by
the core's policy engine. Misdeclaring is the fastest way to lose trust.

## Long-running tools: stream logs and progress

```go
call.Logf("info", "fetching sources for %s", in.Package) // levels: debug/info/warn/error
call.Progress(0.4, "compiling")                          // fraction in [0,1], or -1 indeterminate
```

## Ports: composition between wormholes

Ports connect wormholes to each other; **agents never see them**. There are two
sides. Full detail in `references/composition.md`; the essentials:

**Consume** a port when your tool needs to reach somewhere. Declare it, mark
which tools need it, and read the link in the handler. The core injects a
`<port>_target` argument so the agent picks _which_ admin-configured target the
link routes to:

```go
w.Require(wormhole.Port{Name: "builder", Type: wormhole.PortTypeExecEndpoint,
	Description: "where build commands run"})

wormhole.AddTool(w, wormhole.Tool[buildInput]{
	Name: "build_source_package", Capabilities: []wormhole.Capability{wormhole.CapExecScoped},
	RequiresPorts: []string{"builder"},
	Handler: func(ctx context.Context, call *wormhole.Call, in buildInput) (any, error) {
		link, _ := call.Link("builder")
		var ep wormhole.ExecEndpointDescriptor
		if err := link.DecodeDescriptor(&ep); err != nil { return nil, err }
		r, err := wormhole.DialExecEndpoint(ep)
		if err != nil { return nil, err }
		defer r.Close()
		// Run a fixed, purpose-built command — never one the caller supplied.
		res, err := r.Run(ctx, wormhole.Command{Argv: []string{"dpkg-buildpackage", "-S"}, Dir: in.SourceDir})
		// ... inspect res.Stdout/Stderr/ExitCode, return structured result
		_ = res
		return nil, err
	},
})
```

**Provide** a port when your wormhole _is_ a place others reach. The SDK does
the heavy lifting; you supply only the part specific to you:

| Port type         | Helper                                | You supply                       |
| ----------------- | ------------------------------------- | -------------------------------- |
| `exec-endpoint`   | `ServeExecEndpoint(dir, CommandFunc)` | how to run one command           |
| `network-context` | `ServeNetworkContext(dir, DialFunc)`  | how to dial one address          |
| `mcp-endpoint`    | `ServeMCPEndpoint(dir, MCPBackend)`   | how to call an upstream MCP tool |

```go
w.Provide(
	wormhole.Port{Name: "host", Type: wormhole.PortTypeExecEndpoint},
	func(ctx context.Context, req *wormhole.LinkRequest) (*wormhole.ActiveLink, error) {
		desc, stop, err := wormhole.ServeExecEndpoint(
			wormhole.LinkSocketDir(req.LinkID), wormhole.RunLocalCommand)
		if err != nil { return nil, err }
		return &wormhole.ActiveLink{Descriptor: desc, Close: stop}, nil
	},
)
```

A provider can **also consume** ports: `req.Links` carries the upstream links
the admin routed this one through (e.g. an exec provider that runs through an
upstream `network-context` tunnel, or a container provider that runs on an
upstream host). Mark consumed ports `Optional: true` to degrade gracefully when
unlinked.

### Streaming during a slow link bring-up

A `LinkHandler` that takes minutes (reserving hardware, joining a tunnel) can
stream so the agent isn't left staring at silence:

```go
func openLink(ctx context.Context, req *wormhole.LinkRequest) (*wormhole.ActiveLink, error) {
	req.Progress(-1, "reserving hardware")   // streams on the OpenLink channel
	req.Logf("info", "submitted job %s", id) // before the link is up
	// ... bring the link up, then return ActiveLink
}
```

## Admin side: targets and `via`

An admin binds a provided port to a **target** in the gateway's config. Agents
reference targets by name (`<port>_target`); they never see the config.

```yaml
targets:
  build-box:
    wormhole: ssh
    port: target
    config: { host: build.internal, user: builder }
    idle_timeout: 30m # keep the link warm between calls
    open_timeout: 60m # allow a slow bring-up (e.g. a reservation)
    via:
      net: corp-vpn # route ssh's required network-context through a VPN target
```

`via` nests targets into a chain the core resolves on demand — this is how a
single `builder_target` can fan out to "container → reserved baremetal → ssh →
tailscale" without any wormhole knowing the whole path.

## Build & ship (wormholes repo conventions)

- The root `Makefile` and CI auto-discover any directory with a `go.mod` /
  `Dockerfile`. `make <name>` builds `./bin/<name>`.
- Add a `Dockerfile` by copying an existing one (identical bar the final `CMD`
  binary name). It is an **installer image**: it copies the binary into a
  mounted dir and exits; the gateway loads it.
- **Developing against an unreleased SDK change?** Add the module to `go.work`
  and a `replace github.com/talhaHavadar/interstellar => ../../interstellar` in
  its `go.mod` so `go build`/`go test`/`go mod tidy` resolve the local SDK; drop
  the replace and bump the require once the SDK tags a release.
- Scaffold a new wormhole with `scripts/new-wormhole.sh <name> "<description>"`.

## Checklist before shipping

- [ ] Wormhole name kebab-case; tool names snake_case.
- [ ] Every tool declares its real capabilities; no accidental `CapExecArbitrary`.
- [ ] Inputs are typed structs with a `jsonschema` description on every field.
- [ ] Nothing is written to stdout; logs go through `call.Logf`/stderr.
- [ ] To reach somewhere, a required port — not a host/credential input.
- [ ] `go build && go test ./...`, drop the binary in `--wormhole-dir`, and
      confirm `interstellar__status` reports the tools/ports the way you intend.

## Worked examples in this repo

- `debian-packager/` — typed-ops **consumer** of an `exec-endpoint` (4 tools,
  structured output parsing).
- `contained-debdev/` — `exec-endpoint` **provider** that wraps a container and
  optionally runs through an upstream host (consume + provide; opt-in bootstrap).
- `testflinger/` — provider whose `LinkHandler` does a long reservation and
  streams progress via `req.Progress`/`req.Logf`.
- Minimal references: `interstellar/wormholes/echo`, `local-exec`, `ssh`.

See also `references/composition.md` and `references/capabilities.md`.

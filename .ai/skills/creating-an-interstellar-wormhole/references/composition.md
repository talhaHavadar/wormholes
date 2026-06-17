# Composition: ports, links, and targets

Wormholes stay decoupled by talking over **typed ports** instead of naming each
other. A wormhole declares what it _needs_ (`Require`) and what it _offers_
(`Provide`); an admin wires them together with **targets**; the agent picks a
target per call. No wormhole references another by name.

## Port types and their descriptors

A port has a **type** string. Provider and consumer match on the type and rely
on a stable descriptor schema (`pkg/wormhole/port.go`):

| Type (`PortType…`) | Descriptor                               | Provider serves                                   | Consumer dials                    |
| ------------------ | ---------------------------------------- | ------------------------------------------------- | --------------------------------- |
| `network-context`  | `NetworkContextDescriptor{DialerSocket}` | a SOCKS5 dialer on a unix socket                  | dials through it, tunnel-agnostic |
| `exec-endpoint`    | `ExecEndpointDescriptor{Address}`        | a gRPC exec service for the link's life           | `DialExecEndpoint(...).Run(cmd)`  |
| `mcp-endpoint`     | `MCPEndpointDescriptor{Address}`         | a normalized tool-proxy to an upstream MCP server | `DialMCPEndpoint(...)`            |

Custom types are allowed (lowercase kebab-case), but prefer a well-known type so
wormholes from different authors compose.

## Link lifecycle

1. A tool call names a target for each required port (the core-injected
   `<port>_target` argument).
2. The core resolves the target, recursively bringing up any targets named in
   its `via`, then calls `OpenLink` on the providing wormhole.
3. The provider's `LinkHandler` returns an `ActiveLink{Descriptor, Close}`. The
   descriptor (JSON) is delivered to the consumer as a `Link`; decode it with
   `link.DecodeDescriptor(&T)`.
4. The link is **kept warm** and reference-counted; reused across calls until
   `idle_timeout` after the last release. `Close` runs on teardown.

## Consuming a port

```go
w.Require(wormhole.Port{Name: "builder", Type: wormhole.PortTypeExecEndpoint,
	Optional: false, Description: "where build commands run"})
// in the handler:
link, ok := call.Link("builder")
var ep wormhole.ExecEndpointDescriptor
_ = link.DecodeDescriptor(&ep)
r, _ := wormhole.DialExecEndpoint(ep)
defer r.Close()
res, _ := r.Run(ctx, wormhole.Command{Argv: []string{"…"}, Dir: "…", Env: map[string]string{…}})
// res.Stdout, res.Stderr, res.ExitCode  (a non-zero exit is NOT a Go error)
```

`Command.TimeoutMs` bounds a single command provider-side; `Stdin` is fed then
closed.

## Providing a port (and consuming an upstream)

`req.Links` carries the upstream links the admin routed this link through, so a
provider can be a consumer too. The `ssh` wormhole provides an `exec-endpoint`
and optionally consumes a `network-context`:

```go
func openSSHLink(ctx, req *wormhole.LinkRequest) (*wormhole.ActiveLink, error) {
	dial := directDialer
	if link, ok := findLink(req.Links, wormhole.PortTypeNetworkContext); ok {
		var nc wormhole.NetworkContextDescriptor
		_ = link.DecodeDescriptor(&nc)
		dial, _ = socksDialer(nc.DialerSocket)   // route ssh through the tunnel
	}
	client, _ := connect(ctx, parseConfig(req.Config), dial)
	run := func(ctx, cmd, sink) error { return runOverSSH(ctx, client, cmd, sink) }
	desc, stop, _ := wormhole.ServeExecEndpoint(wormhole.LinkSocketDir(req.LinkID), run)
	return &wormhole.ActiveLink{Descriptor: desc, Close: func() error { stop(); return client.Close() }}, nil
}
```

`req.Config` is admin configuration (JSON) for _this_ link — never from the
agent. Mark a consumed port `Optional: true` and degrade gracefully when it is
absent (connect directly instead of through a tunnel).

## A worked chain

The Debian build stack composes four wormholes through one `builder_target`:

```yaml
targets:
  reserved-trixie: # agent: builder_target=reserved-trixie
    wormhole: contained-debdev # run the build in a container …
    port: exec
    config: { image: …:debian-trixie, ensure_deps: true }
    via: { host: tf-maas } # … on a reserved baremetal
  tf-maas:
    wormhole: testflinger
    port: reserved
    config: { job_file: ~/sandbox/testflinger.yaml, ssh_keys: [gh:me] }
    open_timeout: 60m # the reservation is slow
    idle_timeout: 8h # reserve once, build many times
    via: { orchestrator: tf-box }
  tf-box:
    wormhole: ssh
    port: target
    config: { host: orchestrator.tailnet, user: ci }
    via: { net: tailnet }
  tailnet:
    wormhole: tailscale
    port: tunnel
    config: { authkey_env: TS_AUTHKEY }
```

`debian-packager` only knows it needs an `exec-endpoint`; everything below is
admin configuration. Swap `reserved-trixie` for a local `contained-debdev`
target and the same tool builds locally — no code change.

## Tips

- Keep descriptors stable; consumers depend on their schema.
- Put per-link teardown in `ActiveLink.Close` (stop servers, close clients,
  release reservations) — the core calls it.
- Set `open_timeout` generously for slow bring-ups and `idle_timeout` long when
  a link is expensive to establish.

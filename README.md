# wormholes

A collection of [interstellar](https://github.com/talhaHavadar/interstellar)
wormholes — purpose-built plugins that extend the interstellar gateway.

Wormholes that carry heavy dependencies (a VPN stack, a full Tailscale node,
cloud SDKs) live here rather than in the interstellar core, so the core module
stays lean. This is also where third-party wormholes belong.

## Layout

Each wormhole is its **own Go module** in its own directory, so its
dependencies are isolated — building one never pulls in another's.

```
wormholes/
├── Makefile          # builds any/all wormholes
└── tailscale/        # one wormhole = one module
    ├── go.mod
    └── main.go
```

## Building

The Makefile auto-discovers every directory with a `go.mod`:

```sh
make            # build all wormholes into ./bin/
make tailscale  # build just one
make list       # show discovered wormholes
```

Drop the resulting binary into your interstellar gateway's `--wormhole-dir`
and configure a target for it.

## Smoke-testing a wormhole

A wormhole binary is a `hashicorp/go-plugin` executable — running it directly
prints _"This binary is a plugin. These are not meant to be executed
directly."_ To exercise one, you load it into `interstellard` and drive it
over MCP. For a one-shot manual check without registering with an MCP client,
pipe an `initialize` + `notifications/initialized` + `tools/call` sequence
into `interstellard --stdio`:

```sh
{ cat <<EOF
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"<wormhole>__<tool>","arguments":{ ... }}}
EOF
sleep 3600
} | /path/to/interstellard --stdio --config /path/to/config.yaml 2>&1 \
  | tee /tmp/mcp.jsonl
```

Two gotchas the recipe above avoids:

- **Stdin must stay open** until the tool returns. Bare heredocs close the
  moment the last line is read, and `interstellard` shuts down before your
  call completes. The `{ …; sleep 3600; }` group holds it open; Ctrl-C or
  `pkill sleep` when you're done.
- **Don't put block-buffering awk between `interstellard` and the file.**
  `awk '{print}' > file` block-buffers a few KiB before it writes, so short
  responses (like `initialize`) never land on disk. `tee` line-buffers to
  the terminal, so responses show up as they arrive.

Run `tools/list` first (fast, no port linking needed) to confirm the plumbing
before invoking a long tool call:

```sh
{ cat <<EOF
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
EOF
sleep 3
} | /path/to/interstellard --stdio --config /path/to/config.yaml 2>&1
```

For repeat runs, register the gateway as an MCP server in your agent (e.g.
`claude mcp add interstellar -- /path/to/interstellard --stdio --config …`)
and let the agent speak the protocol.

## Container images

Each wormhole also ships as an **installer image** on GHCR
(`ghcr.io/<owner>/wormhole-<name>`). It carries the wormhole binary and, when
run, copies it into a mounted directory and exits — the interstellar gateway
then loads it. This is how the
[compose deployment](https://github.com/talhaHavadar/interstellar/tree/main/deploy/interstellar-mcp)
adds wormholes with no manual building or copying.

CI (`.github/workflows/build-wormholes.yml`) **auto-discovers** the matrix:
every top-level directory with a `Dockerfile` is built on every push/PR and
published on push to `main` / tags. Multi-arch (`linux/amd64,linux/arm64`), so
no cross-compilation on the user's side.

## Adding a wormhole

1. `mkdir my-wormhole && cd my-wormhole`
2. `go mod init github.com/<you>/wormholes/my-wormhole`
3. Write `main.go` against the
   [`pkg/wormhole`](https://github.com/talhaHavadar/interstellar/tree/main/pkg/wormhole)
   SDK (see the [authoring guide](https://github.com/talhaHavadar/interstellar/blob/main/docs/creating-a-wormhole.md)).
4. Add a `Dockerfile` (copy an existing one — they're identical bar the binary
   name in the final `CMD`).
5. `make my-wormhole` builds the binary; pushing builds & publishes the image.
   Both the Makefile and the CI matrix pick the new directory up automatically.

### Depending on the interstellar SDK

Wormholes import `github.com/talhaHavadar/interstellar/pkg/wormhole`, resolved
as a normal published dependency:

```sh
go get github.com/talhaHavadar/interstellar@latest   # or @main, or a tag
```

The modules here currently pin `interstellar v0.1.0`. To track the SDK's main
branch during active development, `go get github.com/talhaHavadar/interstellar@main`
(it resolves to a pseudo-version, or to the tag when main is exactly tagged).

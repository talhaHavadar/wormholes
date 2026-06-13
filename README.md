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

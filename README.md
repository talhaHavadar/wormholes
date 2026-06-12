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

## Adding a wormhole

1. `mkdir my-wormhole && cd my-wormhole`
2. `go mod init github.com/<you>/wormholes/my-wormhole`
3. Write `main.go` against the
   [`pkg/wormhole`](https://github.com/talhaHavadar/interstellar/tree/main/pkg/wormhole)
   SDK (see the [authoring guide](https://github.com/talhaHavadar/interstellar/blob/main/docs/creating-a-wormhole.md)).
4. `make my-wormhole` — the new module is picked up automatically.

### Depending on the interstellar SDK

Wormholes import `github.com/talhaHavadar/interstellar/pkg/wormhole`. Until
interstellar is published with version tags, modules here resolve the SDK
through a local `replace` directive assuming the repos are siblings:

```
replace github.com/talhaHavadar/interstellar => ../../interstellar
```

Once interstellar is tagged, replace this with a normal versioned `require`.

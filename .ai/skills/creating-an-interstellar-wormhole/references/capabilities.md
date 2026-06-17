# Capabilities

Every tool declares one or more capability classes. They are not advisory: the
core's policy engine enforces them, the audit log records every call with its
declared capabilities, and an admin can allow/deny by class. Declaring honestly
is what lets an operator trust the gateway.

| Constant           | Proto                       | Use it when the tool…                                                              |
| ------------------ | --------------------------- | ---------------------------------------------------------------------------------- |
| `CapRead`          | `CAPABILITY_READ`           | reads state with no side effects (status, fetch, inspect)                          |
| `CapWrite`         | `CAPABILITY_WRITE`          | mutates state through a fixed, parameterized procedure                             |
| `CapNetwork`       | `CAPABILITY_NETWORK`        | establishes or alters connectivity (VPN, tunnel, uscan hitting the network)        |
| `CapExecScoped`    | `CAPABILITY_EXEC_SCOPED`    | runs commands **the wormhole chooses** (e.g. `build_binary_package` runs `sbuild`) |
| `CapExecArbitrary` | `CAPABILITY_EXEC_ARBITRARY` | runs commands **the caller supplies**                                              |

## Choosing

- A tool that runs a _fixed_ command sequence it picked itself is
  `CapExecScoped`, even though it "runs commands". `build_source_package` that
  always runs `dpkg-buildpackage -S` is scoped — the agent supplies parameters
  (distro, source dir), not the command.
- Combine classes when a tool genuinely does several things:
  `check_watch` runs a command _and_ reaches the network →
  `[]wormhole.Capability{wormhole.CapExecScoped, wormhole.CapNetwork}`.
- `CapExecArbitrary` is **denied by default policy**. A tool whose input is "a
  command/script to run" is arbitrary exec and will be invisible to agents until
  an admin explicitly opts the wormhole in. Almost always, the right move is to
  redesign as typed operations, or to push raw execution onto a **port** (raw
  exec travels between wormholes over `exec-endpoint`, never to agents).

## Enforcement

- Declared at registration: `AddTool` validates that at least one capability is
  present and each is valid, panicking on a programmer error at first run.
- The gateway's `policy` config can allow/deny capability classes globally or
  per wormhole; `interstellar__status` reports tools hidden by policy and why.
- Misdeclared capabilities still appear in the audit log against what actually
  ran — there is no hiding, only loss of trust.

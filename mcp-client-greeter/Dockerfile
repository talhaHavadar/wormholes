# syntax=docker/dockerfile:1
#
# Installer image for the mcp-client-greeter wormhole. This is NOT a
# long-running service — it copies the wormhole binary into a directory you
# mount, then exits. The interstellar gateway then loads the binary from that
# directory.
#
#   docker run --rm -v extra-wormholes:/out ghcr.io/talhahavadar/wormhole-mcp-client-greeter
#
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION}" -o /wormhole .

FROM busybox:stable
COPY --from=build /wormhole /wormhole
# Exec-form: copies the binary (preserving its 0755 mode) to the mounted path.
# No shell is invoked. Override the destination by passing a different path as
# the command, e.g. `... /somewhere/else/mcp-client-greeter`.
ENTRYPOINT ["/bin/cp", "-p", "/wormhole"]
CMD ["/out/mcp-client-greeter"]

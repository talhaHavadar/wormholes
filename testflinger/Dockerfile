# syntax=docker/dockerfile:1
#
# Installer image for the testflinger wormhole (same pattern as
# wireguard/Dockerfile): copies the binary into a mounted directory and exits;
# the interstellar gateway loads it from there.
#
#   docker run --rm -v extra-wormholes:/out ghcr.io/talhahavadar/wormhole-testflinger
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
ENTRYPOINT ["/bin/cp", "-p", "/wormhole"]
CMD ["/out/testflinger"]

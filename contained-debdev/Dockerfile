# syntax=docker/dockerfile:1
#
# Installer image for the contained-debdev wormhole (same pattern as
# wireguard/Dockerfile): it copies the wormhole binary into a directory you
# mount, then exits; the interstellar gateway loads it from there. The
# `contained` script is embedded in the binary, so nothing else ships here.
#
#   docker run --rm -v extra-wormholes:/out ghcr.io/talhahavadar/wormhole-contained-debdev
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
CMD ["/out/contained-debdev"]

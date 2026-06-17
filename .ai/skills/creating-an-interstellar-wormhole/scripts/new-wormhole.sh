#!/usr/bin/env bash
#
# Scaffold a new Interstellar wormhole module.
#
#   scripts/new-wormhole.sh <name> "<one-line description>" [module-prefix]
#
# Creates <name>/ with go.mod, main.go (a minimal tool), and a Dockerfile, then
# registers it in ./go.work if one exists. <name> must be lowercase kebab-case.
# module-prefix defaults to the prefix of a sibling wormhole's module, or
# github.com/talhaHavadar/wormholes.
set -euo pipefail

name="${1:?usage: new-wormhole.sh <name> \"<description>\" [module-prefix]}"
desc="${2:?provide a one-line description}"
prefix="${3:-}"

if ! printf '%s' "$name" | grep -Eq '^[a-z][a-z0-9-]{0,63}$'; then
	echo "error: name must be lowercase kebab-case (^[a-z][a-z0-9-]{0,63}\$)" >&2
	exit 1
fi
if [ -e "$name" ]; then
	echo "error: $name already exists" >&2
	exit 1
fi

# Derive the module prefix from a sibling go.mod if not given.
if [ -z "$prefix" ]; then
	sibling="$(find . -maxdepth 2 -name go.mod -not -path './go.work*' | head -1 || true)"
	if [ -n "$sibling" ]; then
		modline="$(grep -m1 '^module ' "$sibling" | awk '{print $2}')"
		prefix="$(dirname "$modline")"
	fi
fi
prefix="${prefix:-github.com/talhaHavadar/wormholes}"
module="$prefix/$name"

mkdir -p "$name"

cat >"$name/go.mod" <<EOF
module $module

go 1.26.4

require github.com/talhaHavadar/interstellar v0.3.0
EOF

cat >"$name/main.go" <<EOF
// Command $name is an Interstellar wormhole.
package main

import (
	"context"

	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

var version = "0.1.0"

type pingInput struct {
	Message string \`json:"message" jsonschema:"text to echo back"\`
}

func main() {
	w := wormhole.New("$name", version, "$desc")

	wormhole.AddTool(w, wormhole.Tool[pingInput]{
		Name:         "ping",
		Description:  "Echo a message back (replace me with a real operation).",
		Capabilities: []wormhole.Capability{wormhole.CapRead},
		Handler: func(ctx context.Context, call *wormhole.Call, in pingInput) (any, error) {
			return map[string]string{"echo": in.Message}, nil
		},
	})

	w.Serve()
}
EOF

cat >"$name/Dockerfile" <<EOF
# syntax=docker/dockerfile:1
# Installer image: copies the wormhole binary into a mounted dir and exits.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=\${VERSION}" -o /wormhole .

FROM busybox:stable
COPY --from=build /wormhole /wormhole
ENTRYPOINT ["/bin/cp", "-p", "/wormhole"]
CMD ["/out/$name"]
EOF

# Register in go.work, if present (keeps the `use (` block sorted-ish).
if [ -f go.work ] && ! grep -q "\./$name\b" go.work; then
	awk -v entry="	./$name" '
		/^use \(/ { print; inblock=1; next }
		inblock && /^\)/ { print entry; inblock=0 }
		{ print }
	' go.work >go.work.tmp && mv go.work.tmp go.work
fi

echo "created $name ($module)"
echo "next: cd $name && go mod tidy && go build ./... && go test ./..."
echo "then edit main.go — replace the placeholder 'ping' tool with a real, typed operation."

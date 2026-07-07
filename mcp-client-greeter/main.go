// Command mcp-client-greeter is a REFERENCE wormhole — a teaching example, not a
// production use case. It exists to demonstrate the intended way to integrate a
// third-party MCP server (see interstellar ADR 0001): the upstream server is a
// backend reached through a required mcp-endpoint, and the agent sees only this
// wormhole's own purpose-built, honestly-classified tool — never the upstream's
// raw tools. Copy its shape; do not deploy it expecting it to do anything
// useful.
//
// It expects an upstream MCP server that advertises a "greet" tool (the
// interstellar repo ships a tiny mcp-greeter example server for this). Whatever
// else the upstream exposes, the agent's reach is exactly what is authored
// here: curation, not forwarding, is the whole point.
//
// It is transport-agnostic — hence "client", not "stdio", in the name. It only
// consumes an mcp-endpoint and never learns how the upstream is reached, so the
// same wormhole works behind any provider: mcp-stdio today, an mcp-http
// provider later, with no change here.
//
// Wire it by binding a provider's "server" port to the upstream (mcp-stdio
// below), then routing this wormhole's "upstream" port to that target:
//
//	targets:
//	  greeter-backend:
//	    wormhole: mcp-stdio
//	    port: server
//	    config:
//	      command: mcp-greeter
package main

import (
	"context"
	"fmt"

	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

var version = "dev" // overridden at link time via -ldflags "-X main.version=..."

type greetInput struct {
	// Name of the person to greet.
	Name string `json:"name" jsonschema:"the name to greet"`
}

type greetOutput struct {
	Greeting string `json:"greeting"`
}

func main() {
	w := wormhole.New("mcp-client-greeter", version,
		"Reference example: greets a person, backed by an upstream MCP server.")

	w.Require(wormhole.Port{
		Name:        "upstream",
		Type:        wormhole.PortTypeMCPEndpoint,
		Description: "the MCP server that performs the greeting",
	})

	wormhole.AddTool(w, wormhole.Tool[greetInput]{
		Name:          "greet",
		Description:   "Greet a person by name.",
		Capabilities:  []wormhole.Capability{wormhole.CapRead},
		RequiresPorts: []string{"upstream"},
		Handler:       greet,
	})

	w.Serve()
}

func greet(ctx context.Context, call *wormhole.Call, in greetInput) (any, error) {
	if in.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	link, ok := call.Link("upstream")
	if !ok {
		return nil, fmt.Errorf("no mcp endpoint linked")
	}
	var ep wormhole.MCPEndpointDescriptor
	if err := link.DecodeDescriptor(&ep); err != nil {
		return nil, fmt.Errorf("decoding mcp endpoint: %w", err)
	}
	proxy, err := wormhole.DialMCPEndpoint(ep)
	if err != nil {
		return nil, err
	}
	defer proxy.Close()

	// Call exactly one upstream tool, with arguments we control. The agent
	// never names the upstream tool or shapes its arguments.
	res, err := proxy.CallTool(ctx, "greet", map[string]any{"name": in.Name})
	if err != nil {
		return nil, fmt.Errorf("calling upstream greet: %w", err)
	}
	if res.IsError {
		return nil, fmt.Errorf("upstream greet failed: %s", res.Text())
	}
	return greetOutput{Greeting: res.Text()}, nil
}

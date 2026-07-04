package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/talhaHavadar/interstellar/pkg/wormhole"
)

// heartbeatInterval is how often startHeartbeat emits a Progress update while
// a builder command runs. The MCP harness drops tool calls that go silent on
// the notification channel for too long, and the long stages (sbuild,
// fetch_orig, licensecheck) can run for many minutes — sbuild for hours —
// without producing any. r.Run buffers exec output internally, so the
// underlying gRPC traffic doesn't keep the harness happy on its own.
const heartbeatInterval = 20 * time.Second

// startHeartbeat emits an indeterminate Progress update every interval until
// the returned stop func runs. Safe to call stop multiple times. Concurrent
// with the caller's own use of call (the wormhole pkg serializes emits).
func startHeartbeat(call *wormhole.Call, label string, interval time.Duration) func() {
	done := make(chan struct{})
	start := time.Now()
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				elapsed := time.Since(start).Round(time.Second)
				call.Progress(-1, fmt.Sprintf("%s still running (%s elapsed)", label, elapsed))
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

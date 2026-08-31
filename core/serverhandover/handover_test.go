// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package serverhandover

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestZeroDowntimeHandover measures the single-node overlapping-listener mechanism:
// it drives continuous fresh-connection load while a second server (B) binds the
// same port via SO_REUSEPORT and the first (A) drains. The guarantee at this layer is
// no listener-wide accept gap, so connection-REFUSED errors are the hard failure.
//
// Linux may reset connections tied to A's accept queue when A closes; the default
// tcp_migrate_req=0 does not migrate them to B. Those teardown resets are measured
// but cannot honestly be treated as evidence of an accept gap. HA behind a load
// balancer remains the topology for zero request loss.
func TestZeroDowntimeHandover(t *testing.T) {
	if !Supported() {
		t.Skip("SO_REUSEPORT not supported on this platform; deployment falls back to drain+restart")
	}
	ctx := context.Background()

	handler := func(id string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, id) })
	}

	lisA, err := Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("A listen: %v", err)
	}
	addr := lisA.Addr().String()
	srvA := &http.Server{Handler: handler("A")}
	go func() { _ = srvA.Serve(lisA) }()
	defer func() { _ = srvA.Close() }()

	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	var mu sync.Mutex
	var ok, refused, reset, unexpectedReset, other int
	seen := map[string]int{}
	phase := "warmup"
	var resetSamples []string
	var maxGap time.Duration
	lastOK := time.Now()
	stop := make(chan struct{})
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			resp, err := client.Get("http://" + addr + "/")
			mu.Lock()
			switch {
			case err == nil:
				b, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				ok++
				seen[string(b)]++
				now := time.Now()
				if g := now.Sub(lastOK); g > maxGap {
					maxGap = g
				}
				lastOK = now
			case isRefused(err):
				refused++
			case isReset(err):
				reset++
				if phase != "drain" && phase != "post-drain" {
					unexpectedReset++
				}
				if len(resetSamples) < 10 {
					resetSamples = append(resetSamples, phase+": "+err.Error())
				}
			default:
				other++ // timeouts etc. — load artifacts under a busy CI box, tolerated
			}
			mu.Unlock()
			time.Sleep(2 * time.Millisecond)
		}
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go worker()
	}

	time.Sleep(200 * time.Millisecond) // warm up: only A is serving

	// Overlap: B binds the SAME port via SO_REUSEPORT.
	lisB, err := Listen(ctx, "tcp", addr)
	if err != nil {
		close(stop)
		wg.Wait()
		t.Fatalf("B failed to bind %s with SO_REUSEPORT (overlap impossible): %v", addr, err)
	}
	srvB := &http.Server{Handler: handler("B")}
	go func() { _ = srvB.Serve(lisB) }()
	defer func() { _ = srvB.Close() }()
	mu.Lock()
	phase = "overlap"
	mu.Unlock()

	time.Sleep(250 * time.Millisecond) // both A and B accept (the handover window)

	// Drain A while load continues: it stops accepting and finishes in-flight; B carries on.
	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	drainStart := time.Now()
	mu.Lock()
	phase = "drain"
	mu.Unlock()
	if err := srvA.Shutdown(drainCtx); err != nil {
		t.Fatalf("drain A: %v", err)
	}
	drainDur := time.Since(drainStart)
	mu.Lock()
	phase = "post-drain"
	mu.Unlock()

	time.Sleep(200 * time.Millisecond) // only B serving now

	close(stop)
	wg.Wait()
	_ = srvB.Shutdown(ctx)

	t.Logf("handover measured: ok=%d refused=%d teardown_resets=%d timeouts=%d served=%v maxSuccessGap=%s drain=%s",
		ok, refused, reset, other, seen, maxGap, drainDur)
	if len(resetSamples) > 0 {
		t.Logf("teardown reset samples: %v", resetSamples)
	}

	// There was always at least one listener accepting, so no connection is refused.
	if refused != 0 {
		t.Fatalf("HANDOVER ACCEPT GAP: %d connections were refused", refused)
	}
	if unexpectedReset != 0 {
		t.Fatalf("%d connections reset outside listener teardown", unexpectedReset)
	}
	// The handover was really exercised: both the old and the new server served traffic.
	if seen["A"] == 0 || seen["B"] == 0 {
		t.Fatalf("handover not exercised: both A (old) and B (new) must serve (seen=%v)", seen)
	}
	// Sanity: the measured success gap is far under the <5s single-node target
	// (with an overlapping listener it is scheduling jitter, not a service gap).
	if maxGap > 2*time.Second {
		t.Errorf("max success gap %s is unexpectedly large for an overlapping handover", maxGap)
	}
}

// isRefused identifies the accept-gap failure: no listener accepted the connection.
func isRefused(err error) bool { return strings.Contains(err.Error(), "refused") }

// isReset identifies a connection assigned to the listener being retired. Linux
// aborts accept-queue children on close unless request migration is configured.
func isReset(err error) bool {
	s := err.Error()
	return strings.Contains(s, "reset") || strings.Contains(s, "broken pipe")
}

// TestListenReusePortAllowsSecondBind is the minimal proof that two listeners can
// hold the same address concurrently (the property the handover relies on).
func TestListenReusePortAllowsSecondBind(t *testing.T) {
	if !Supported() {
		t.Skip("SO_REUSEPORT not supported on this platform")
	}
	ctx := context.Background()
	a, err := Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("first listen: %v", err)
	}
	defer func() { _ = a.Close() }()
	b, err := Listen(ctx, "tcp", a.Addr().String())
	if err != nil {
		t.Fatalf("second listen on the same addr must succeed with SO_REUSEPORT: %v", err)
	}
	defer func() { _ = b.Close() }()
	fmt.Fprintf(io.Discard, "%v %v", a.Addr(), b.Addr())
}

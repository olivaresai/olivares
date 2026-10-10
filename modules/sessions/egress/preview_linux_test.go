// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package egress

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// echoServer answers every connection with what it reads and counts them, so a
// test can tell a refused relay from an unreachable address.
func echoServer(t *testing.T) (net.Listener, *atomic.Int32) {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	var accepted atomic.Int32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return l, &accepted
}

// The preview relay dials only loopback ports that are not the bridge's own.
func TestPreviewRelayReachesOnlySessionLoopbackPorts(t *testing.T) {
	dir, err := os.MkdirTemp("", "pv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "p.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	app, appHits := echoServer(t)
	// The bridge's own listener answers too: only the relay's refusal keeps it out.
	bridge, bridgeHits := echoServer(t)
	go previewAccept(l, map[string]bool{portOf(bridge.Addr()): true})

	roundTrip := func(address string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		c, err := DialPreview(ctx, socket, address)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.WriteString(c, "ping")
		buf := make([]byte, 4)
		n, _ := io.ReadFull(c, buf)
		return string(buf[:n])
	}
	if got := roundTrip(app.Addr().String()); got != "ping" || appHits.Load() != 1 {
		t.Fatalf("session port = %q (%d accepts), want the app's echo", got, appHits.Load())
	}
	_, appPort, _ := net.SplitHostPort(app.Addr().String())
	_, bridgePort, _ := net.SplitHostPort(bridge.Addr().String())
	for _, refused := range []string{
		bridge.Addr().String(),     // the bridge's own port, reachable on loopback
		"127.0.0.1:0" + bridgePort, // the same port, spelled with a leading zero
		"localhost:" + appPort,     // a name, even one that resolves to the app
		"192.0.2.1:80",             // not loopback
		"not-an-address",
	} {
		if got := roundTrip(refused); got != "" {
			t.Fatalf("%s answered %q", refused, got)
		}
	}
	if appHits.Load() != 1 || bridgeHits.Load() != 0 {
		t.Fatalf("a refused address was dialed: app %d accepts, bridge %d", appHits.Load(), bridgeHits.Load())
	}
}

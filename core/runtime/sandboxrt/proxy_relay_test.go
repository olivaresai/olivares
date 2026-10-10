// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandboxrt

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProxyRelayClosesActiveConnections(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := upstream.Accept(); accepted <- c }()
	path := filepath.Join(t.TempDir(), "proxy.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closeRelay, err := startProxyRelay(ctx, path, upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer closeRelay()
	guest, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer guest.Close()
	if _, err := guest.Write([]byte("PING")); err != nil {
		t.Fatal(err)
	}
	var proxy net.Conn
	select {
	case proxy = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("no upstream connection")
	}
	if proxy == nil {
		t.Fatal("upstream accept failed")
	}
	defer proxy.Close()
	_ = proxy.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(proxy, buf); err != nil || string(buf) != "PING" {
		t.Fatalf("relay read = %q, %v", buf, err)
	}
	cancel()
	done := make(chan struct{})
	go func() { closeRelay(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("active relay prevented cleanup")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("relay socket persists: %v", err)
	}
	if _, err := guest.Read(buf); err != io.EOF {
		t.Fatalf("guest remained connected after cleanup: %v", err)
	}
}

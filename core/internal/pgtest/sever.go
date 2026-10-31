// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package pgtest

import (
	"context"
	"io"
	"net"
	"net/url"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/sdk/netbind"
)

// SeverProxy is a loopback TCP forwarder in front of one PostgreSQL server that
// can cut every connection through it without telling either side first. It is
// the reproducer for a pool reset the engine did not cause and was not told
// about: the pool still holds its connections, the server is still
// up, and the next request on a pooled connection finds it dead. It forwards
// every byte untouched, so the server and the driver are the real ones.
type SeverProxy struct {
	ln       net.Listener
	upstream string
	dsn      string

	mu     sync.Mutex
	conns  []net.Conn
	closed bool
}

// NewSeverProxy forwards 127.0.0.1:<ephemeral> to the host of dsn, a libpq URL
// such as the App field of DSNs. Point the pool under test at the proxy's DSN.
func NewSeverProxy(tb testing.TB, dsn string) *SeverProxy {
	tb.Helper()
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		tb.Fatalf("SeverProxy needs a libpq URL with a host: %v", err)
	}
	ln, err := netbind.Listen(context.Background(), "tcp", "127.0.0.1:0", netbind.Policy{
		Component: "pgtest",
		Purpose:   "severable PostgreSQL proxy",
		OptIn:     "(none: this listener is loopback by construction)",
	})
	if err != nil {
		tb.Fatalf("SeverProxy listen: %v", err)
	}
	p := &SeverProxy{ln: ln, upstream: u.Host}
	u.Host = ln.Addr().String()
	p.dsn = u.String()
	go p.serve()
	tb.Cleanup(func() {
		_ = ln.Close()
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		p.Sever()
	})
	return p
}

// DSN is the original DSN with its host replaced by the proxy's address.
func (p *SeverProxy) DSN() string { return p.dsn }

// Sever closes both sides of every connection forwarded so far. The server is
// untouched: a connection opened afterwards works.
func (p *SeverProxy) Sever() {
	p.mu.Lock()
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (p *SeverProxy) serve() {
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return
		}
		server, err := net.Dial("tcp", p.upstream)
		if err != nil {
			_ = client.Close()
			continue
		}
		p.mu.Lock()
		if p.closed {
			// Accepted as the test ended: nothing may outlive Sever.
			p.mu.Unlock()
			_ = client.Close()
			_ = server.Close()
			return
		}
		p.conns = append(p.conns, client, server)
		p.mu.Unlock()
		go func() { _, _ = io.Copy(server, client); _ = server.Close() }()
		go func() { _, _ = io.Copy(client, server); _ = client.Close() }()
	}
}

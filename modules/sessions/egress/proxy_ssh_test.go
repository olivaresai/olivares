// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package egress

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// sshFixture answers like an SSH server's first bytes, then echoes.
func sshFixture(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = c.Write([]byte("SSH-2.0-fixture\r\n"))
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					_, _ = c.Write([]byte("echo " + line))
				}
			}()
		}
	}()
	return l
}

// startSSHProxy starts a proxy whose bound provider is api.z.ai and whose
// declared host listed.example is granted https and ssh. Every dial lands on the
// fixture and is counted.
func startSSHProxy(t *testing.T, fixture net.Listener, dials *atomic.Int32) *proxy {
	t.Helper()
	ds, err := destinations(Policy{Providers: []string{"https://api.z.ai/"}, PublicProviders: []string{"https://listed.example/", "ssh://listed.example/"}})
	if err != nil {
		t.Fatal(err)
	}
	resolver := hostResolver{"api.z.ai": net.ParseIP("93.184.216.35"), "listed.example": net.ParseIP("93.184.216.34")}
	p, err := startProxyWithDialer(context.Background(), ds, resolver, func(ctx context.Context, network, _ string, timeout time.Duration) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, fixture.Addr().String())
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	return p
}

// connectStatus sends CONNECT to the proxy's socket and returns the status line
// and the connection, positioned after the response headers.
func connectStatus(t *testing.T, p *proxy, authority string) (string, net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.DialTimeout("unix", p.socket, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", authority, authority); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(c)
	status, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("CONNECT %s: no response: %v", authority, err)
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil || line == "\r\n" {
			break
		}
	}
	return strings.TrimSpace(status), c, r
}

func TestProxyTunnelsRawBytesToADeclaredSSHHost(t *testing.T) {
	var dials atomic.Int32
	p := startSSHProxy(t, sshFixture(t), &dials)
	status, c, r := connectStatus(t, p, "listed.example:22")
	if status != "HTTP/1.1 200 Connection Established" {
		t.Fatalf("CONNECT listed.example:22: %q, want 200", status)
	}
	if banner, err := r.ReadString('\n'); err != nil || banner != "SSH-2.0-fixture\r\n" {
		t.Fatalf("server banner through the tunnel: %q, %v", banner, err)
	}
	if _, err := c.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	if reply, err := r.ReadString('\n'); err != nil || reply != "echo ping\n" {
		t.Fatalf("bytes through the tunnel: %q, %v", reply, err)
	}
	if dials.Load() != 1 {
		t.Fatalf("dials = %d, want 1", dials.Load())
	}
}

func TestProxyRefusesEverySSHTunnelNotDeclared(t *testing.T) {
	for _, authority := range []string{
		"listed.example:2222", // a declared host, another port
		"listed.example:80",
		"api.z.ai:22",      // the bound provider is https only
		"other.example:22", // not declared
		"93.184.216.34:22", // a declared host's address is not its name
	} {
		t.Run(authority, func(t *testing.T) {
			var dials atomic.Int32
			p := startSSHProxy(t, sshFixture(t), &dials)
			status, _, _ := connectStatus(t, p, authority)
			if strings.Contains(status, " 200 ") {
				t.Fatalf("CONNECT %s admitted: %q", authority, status)
			}
			if dials.Load() != 0 {
				t.Fatalf("CONNECT %s dialed %d times, want 0", authority, dials.Load())
			}
		})
	}
}

// A declared ssh host is not an HTTP origin: the plain-request path stays closed,
// whatever scheme the request URL names.
func TestProxySSHHostServesNoPlainHTTP(t *testing.T) {
	for _, target := range []string{"http://listed.example:22/", "ssh://listed.example:22/", "https://listed.example:22/"} {
		t.Run(target, func(t *testing.T) {
			var dials atomic.Int32
			p := startSSHProxy(t, sshFixture(t), &dials)
			c, err := net.Dial("unix", p.socket)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			_, _ = fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: listed.example:22\r\n\r\n", target)
			status, _ := bufio.NewReader(c).ReadString('\n')
			if !strings.Contains(status, " 403 ") || dials.Load() != 0 {
				t.Fatalf("GET %s: %q, dials %d, want 403 and none", target, status, dials.Load())
			}
		})
	}
}

func TestProxyCloseTerminatesSSHTunnel(t *testing.T) {
	var dials atomic.Int32
	p := startSSHProxy(t, sshFixture(t), &dials)
	status, _, r := connectStatus(t, p, "listed.example:22")
	if !strings.Contains(status, " 200 ") {
		t.Fatalf("CONNECT: %q", status)
	}
	if _, err := r.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	p.close()
	if _, err := r.ReadByte(); err == nil {
		t.Fatal("the ssh tunnel survived proxy close")
	}
}

func TestSSHEndpointsAreForListedHostsOnly(t *testing.T) {
	ds, err := destinations(Policy{Providers: []string{"https://api.z.ai/"}, PublicProviders: []string{"ssh://listed.example/", "ssh://other.example:2200/"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 3 || ds[1].scheme != "ssh" || ds[1].port != "22" || !ds[1].listed || ds[2].port != "2200" {
		t.Fatalf("destinations = %+v, want ssh on 22 and 2200, listed", ds)
	}
	for name, policy := range map[string]Policy{
		"provider": {Providers: []string{"ssh://listed.example/"}},
		"control":  {Providers: []string{"https://api.z.ai/"}, Controls: []string{"ssh://127.0.0.1/"}},
		"offline":  {Offline: true, PublicProviders: []string{"ssh://listed.example/"}},
	} {
		if _, err := destinations(policy); err == nil {
			t.Errorf("%s: an ssh endpoint was admitted", name)
		}
	}
}

// 1 provider, 3 relays, 8 registries and 4 declared hosts on https and ssh.
func TestEndpointBoundFitsAProfileAtItsLimits(t *testing.T) {
	policy := Policy{Providers: []string{"https://api.z.ai/"}}
	for i := 0; i < 3; i++ {
		policy.Controls = append(policy.Controls, fmt.Sprintf("http://127.0.0.1:%d/", 9000+i))
	}
	for i := 0; i < 8; i++ {
		policy.PublicProviders = append(policy.PublicProviders, fmt.Sprintf("https://registry%d.example/", i))
	}
	for i := 0; i < 4; i++ {
		policy.PublicProviders = append(policy.PublicProviders, fmt.Sprintf("https://host%d.example/", i), fmt.Sprintf("ssh://host%d.example/", i))
	}
	if _, err := destinations(policy); err != nil {
		t.Fatalf("a full profile: %v", err)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package egress

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProxyOnlyBoundEndpoint(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://127.0.0.2:1/", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "provider")
	}))
	defer provider.Close()
	ds, err := destinations(Policy{Providers: []string{provider.URL}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := startProxy(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	u, _ := url.Parse("http://proxy")
	transport := &http.Transport{Proxy: http.ProxyURL(u), DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p.socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for _, tc := range []struct {
		url  string
		code int
	}{
		{provider.URL, http.StatusOK},
		{strings.Replace(provider.URL, "127.0.0.1", "127.0.0.2", 1), http.StatusForbidden},
		{"http://127.0.0.1:1/", http.StatusForbidden},
		{"http://[::1]:1/", http.StatusForbidden},
		{"http://api.anthropic.com/", http.StatusForbidden},
		{provider.URL + "/redirect", http.StatusForbidden},
	} {
		resp, err := client.Get(tc.url)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.code {
			t.Errorf("%s: status %d, want %d", tc.url, resp.StatusCode, tc.code)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("provider calls=%d, want 2", got)
	}
	info, err := os.Stat(p.socket)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket permissions: %v %v", info, err)
	}
	info, err = os.Stat(p.dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("socket directory permissions: %v %v", info, err)
	}
}

func TestProxyCloseTerminatesConnectTunnel(t *testing.T) {
	stopped := make(chan struct{})
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ready\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(stopped)
	}))
	defer provider.Close()
	ds, err := destinations(Policy{Providers: []string{provider.URL}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := startProxy(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	roots := x509.NewCertPool()
	roots.AddCert(provider.Certificate())
	p.transport.TLSClientConfig.RootCAs = roots
	clientRoots := x509.NewCertPool()
	if !clientRoots.AppendCertsFromPEM(p.caPEM) {
		t.Fatal("no session CA")
	}
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "proxy"}), TLSClientConfig: &tls.Config{RootCAs: clientRoots}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p.socket)
	}}
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Get(provider.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	if line, err := reader.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("TLS stream: %q %v", line, err)
	}
	p.close()
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("TLS tunnel survived proxy close")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("upstream tunnel survived proxy close")
	}
	if _, err := os.Stat(p.dir); !os.IsNotExist(err) {
		t.Fatalf("proxy directory survives: %v", err)
	}
}

func TestControlRelayDoesNotGrantEngineTunnel(t *testing.T) {
	ds, err := destinations(Policy{Providers: []string{"https://provider.example"}, Controls: []string{"http://127.0.0.1:8447/session/mcp"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, auth string
		allowed            bool
	}{
		{"POST", "/session/mcp", "Bearer fixture", true},
		{"POST", "/session/mcp", "", false},
		{"POST", "/v1/inference", "Bearer fixture", false},
		{"POST", "/session/mcp/../v1/inference", "Bearer fixture", false},
		{"POST", "/session/%6dcp", "Bearer fixture", false},
		{"CONNECT", "/session/mcp", "Bearer fixture", false},
	} {
		r := httptest.NewRequest(tc.method, "http://127.0.0.1:8447"+tc.path, nil)
		r.Header.Set("Authorization", tc.auth)
		if got := requestAllowed(r, ds); got != tc.allowed {
			t.Errorf("%s %s: allowed=%v", tc.method, tc.path, got)
		}
	}
}

func TestProxyPrivateHostname(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "provider")
	}))
	defer provider.Close()
	endpoint := strings.Replace(provider.URL, "127.0.0.1", "localhost", 1)
	ds, err := destinations(Policy{Providers: []string{endpoint}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := startProxy(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	u, _ := url.Parse("http://proxy")
	transport := &http.Transport{Proxy: http.ProxyURL(u), DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p.socket)
	}}
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bound private hostname: status %d", resp.StatusCode)
	}
}

// An administrator-listed host is not a bound provider: whatever it resolves
// to, a loopback or private address is not admitted.
func TestProxyPublicOnlyRefusesPrivateResolution(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer target.Close()
	endpoint := strings.Replace(target.URL, "127.0.0.1", "listed.example", 1)
	ds, err := destinations(Policy{Providers: []string{endpoint}, PublicOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := startProxyWithResolver(context.Background(), ds, &changingResolver{ip: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	u, _ := url.Parse("http://proxy")
	transport := &http.Transport{Proxy: http.ProxyURL(u), DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p.socket)
	}}
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	// Smokescreen answers an address-class denial with 407, an ACL denial with 403.
	if (resp.StatusCode != http.StatusProxyAuthRequired && resp.StatusCode != http.StatusForbidden) || hits.Load() != 0 {
		t.Fatalf("public-only host reached a loopback address: status %d hits %d", resp.StatusCode, hits.Load())
	}
}

type changingResolver struct {
	ip    net.IP
	first net.IP
	calls int
}

func (r *changingResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	r.calls++
	if r.first != nil {
		return []net.IP{r.first, append(net.IP(nil), r.ip...)}, nil
	}
	return []net.IP{append(net.IP(nil), r.ip...)}, nil
}
func (*changingResolver) LookupPort(ctx context.Context, network, port string) (int, error) {
	return net.DefaultResolver.LookupPort(ctx, network, port)
}

func TestProxyPinsPrivateDNSForSession(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "provider") }))
	defer provider.Close()
	endpoint := strings.Replace(provider.URL, "127.0.0.1", "ollama.internal", 1)
	ds, err := destinations(Policy{Providers: []string{endpoint}})
	if err != nil {
		t.Fatal(err)
	}
	resolver := &changingResolver{ip: net.ParseIP("127.0.0.1"), first: net.ParseIP("127.0.0.2")}
	p, err := startProxyWithResolver(context.Background(), ds, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	resolver.ip = net.ParseIP("127.0.0.2")
	u, _ := url.Parse("http://proxy")
	transport := &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p.socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for _, target := range []string{endpoint, endpoint, strings.Replace(endpoint, "ollama.internal", "other.internal", 1), "http://ollama.internal:1"} {
		resp, err := client.Get(target)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		want := http.StatusForbidden
		if target == endpoint {
			want = http.StatusOK
		}
		if resp.StatusCode != want {
			t.Errorf("%s: status %d, want %d", target, resp.StatusCode, want)
		}
	}
	if resolver.calls != 1 {
		t.Fatalf("session re-resolved provider %d times", resolver.calls)
	}
}

func TestProxyIPv6OnlyLocalhost(t *testing.T) {
	l, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 is unavailable: %v", err)
	}
	provider := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	_ = provider.Listener.Close()
	provider.Listener = l
	provider.Start()
	defer provider.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	endpoint := "http://localhost:" + port
	ds, err := destinations(Policy{Providers: []string{endpoint}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := startProxy(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	u, _ := url.Parse("http://proxy")
	transport := &http.Transport{Proxy: http.ProxyURL(u), DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p.socket)
	}}
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("IPv6-only provider: status %d", resp.StatusCode)
	}
}

func TestProxyRejectsSharedAddressAuthority(t *testing.T) {
	for _, inner := range []struct{ name, sni, host string }{
		{"SNI", "other.example", "other.example"},
		{"HTTPHost", "provider.example", "other.example"},
	} {
		t.Run(inner.name, func(t *testing.T) {
			var foreign atomic.Int32
			provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.Host, "other.example:") {
					foreign.Add(1)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer provider.Close()
			_, port, _ := net.SplitHostPort(provider.Listener.Addr().String())
			address := net.JoinHostPort("provider.example", port)
			ds, err := destinations(Policy{Providers: []string{"https://" + address}})
			if err != nil {
				t.Fatal(err)
			}
			p, err := startProxyWithResolver(context.Background(), ds, &changingResolver{ip: net.ParseIP("127.0.0.1")})
			if err != nil {
				t.Fatal(err)
			}
			defer p.close()
			upstreamRoots := x509.NewCertPool()
			upstreamRoots.AddCert(provider.Certificate())
			p.transport.TLSClientConfig.RootCAs = upstreamRoots
			p.transport.TLSClientConfig.ServerName = "127.0.0.1"
			roots := x509.NewCertPool()
			roots.AppendCertsFromPEM(p.caPEM)
			transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "proxy"}), TLSClientConfig: &tls.Config{RootCAs: roots}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", p.socket)
			}}
			defer transport.CloseIdleConnections()
			positive, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Get("https://" + address)
			if err != nil {
				t.Fatal(err)
			}
			_ = positive.Body.Close()
			if positive.StatusCode != http.StatusOK {
				t.Fatalf("bound TLS request: %d", positive.StatusCode)
			}
			c, err := net.DialTimeout("unix", p.socket, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(3 * time.Second))
			_, _ = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", address, address)
			resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "CONNECT"})
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode == http.StatusOK {
				// The adversarial child deliberately ignores certificate verification.
				conn := tls.Client(c, &tls.Config{InsecureSkipVerify: true, ServerName: inner.sni})
				if err := conn.Handshake(); err == nil {
					_, _ = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", net.JoinHostPort(inner.host, port))
					response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "GET"})
					if err == nil {
						_ = response.Body.Close()
					}
				}
				_ = conn.Close()
			}
			if n := foreign.Load(); n != 0 {
				t.Fatalf("shared-address foreign host received %d requests", n)
			}
		})
	}
}

func TestProxyCheckedPublicFailover(t *testing.T) {
	for _, fallback := range []struct {
		ip      string
		allowed bool
	}{
		{"192.0.2.2", true}, {"2001:db8::2", true},
		{"169.254.169.254", false}, {"100.64.0.1", false},
		{"64:ff9b::a9fe:a9fe", false}, {"2002:a9fe:a9fe::1", false}, {"2001::1", false},
	} {
		t.Run(fallback.ip, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
			defer provider.Close()
			ds, err := destinations(Policy{Providers: []string{"http://provider.example:8080"}})
			if err != nil {
				t.Fatal(err)
			}
			resolver := &changingResolver{first: net.ParseIP("192.0.2.1"), ip: net.ParseIP(fallback.ip)}
			var attempts []string
			p, err := startProxyWithDialer(context.Background(), ds, resolver, func(ctx context.Context, network, address string, timeout time.Duration) (net.Conn, error) {
				attempts = append(attempts, address)
				if address == "192.0.2.1:8080" {
					return nil, errors.New("fixture first public address unavailable")
				}
				return (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, provider.Listener.Addr().String())
			})
			if err != nil {
				t.Fatal(err)
			}
			defer p.close()
			// A later DNS answer must not become a failover candidate.
			resolver.ip = net.ParseIP("203.0.113.99")
			transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "proxy"}), DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", p.socket)
			}}
			defer transport.CloseIdleConnections()
			resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Get("http://provider.example:8080/")
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if got := resp.StatusCode == http.StatusOK; got != fallback.allowed {
				t.Errorf("status=%d, allowed=%v", resp.StatusCode, fallback.allowed)
			}
			want := 1
			if fallback.allowed {
				want = 2
			}
			if len(attempts) != want {
				t.Errorf("dial attempts=%v, want %d", attempts, want)
			}
			if len(attempts) > 1 && attempts[1] != net.JoinHostPort(fallback.ip, "8080") {
				t.Errorf("unpinned failover: %v", attempts)
			}
			if resolver.calls != 1 {
				t.Errorf("DNS resolved %d times, want 1", resolver.calls)
			}
		})
	}
}

func TestProxyTLSVerifiesUpstream(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer provider.Close()
	ds, err := destinations(Policy{Providers: []string{provider.URL}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := startProxy(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(p.caPEM)
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "proxy"}), TLSClientConfig: &tls.Config{RootCAs: roots}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p.socket)
	}}
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Get(provider.URL)
	if err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("untrusted upstream certificate accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("request reached untrusted TLS server")
	}
	other, err := startProxy(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	defer other.close()
	if string(p.caPEM) == string(other.caPEM) {
		t.Fatal("CA shared between sessions")
	}
}

func TestProxyDefaultTLSAuthority(t *testing.T) {
	ds, err := destinations(Policy{Providers: []string{"https://provider.example"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, authority := range []string{"provider.example", "provider.example:443", "PROVIDER.example"} {
		req := httptest.NewRequest("GET", "https://provider.example:443/v1/messages", nil)
		req.Host = authority
		if !requestAllowed(req, ds) {
			t.Errorf("valid default TLS authority refused: %s", authority)
		}
	}
}

func TestProxyFailedTLSHandshakeReleasesConnections(t *testing.T) {
	ds, err := destinations(Policy{Providers: []string{"https://provider.example:443"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := startProxyWithResolver(context.Background(), ds, &changingResolver{ip: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	for _, cfg := range []*tls.Config{
		{ServerName: "other.example", InsecureSkipVerify: true}, // adversarial peer
		{ServerName: "provider.example", NextProtos: []string{"h2"}, InsecureSkipVerify: true},
		{ServerName: "provider.example", MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11, InsecureSkipVerify: true},
	} {
		c, err := net.DialTimeout("unix", p.socket, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = io.WriteString(c, "CONNECT provider.example:443 HTTP/1.1\r\nHost: provider.example:443\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "CONNECT"})
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT: %v %v", resp, err)
		}
		if err := tls.Client(c, cfg).Handshake(); err == nil {
			t.Fatal("unsupported TLS accepted")
		}
	}
	// Clients remain open and the session stays alive: cleanup must happen on
	// the proxy's failed-handshake path, not by deferred client/session Close.
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		p.mu.Lock()
		count := len(p.connections)
		p.mu.Unlock()
		if count == 0 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("%d failed handshake connections retained", count)
		case <-tick.C:
		}
	}
}

// The CA key only signs; the certificate a client sees in the handshake has its own key.
func TestSessionTLSLeafKeyIsNotTheCAKey(t *testing.T) {
	_, cfg, err := sessionTLS([]destination{{host: "provider.example", port: "443", scheme: "https"}})
	if err != nil {
		t.Fatal(err)
	}
	chain := cfg.Certificates[0].Certificate
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(chain[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.CheckSignatureFrom(ca); err != nil {
		t.Fatal(err)
	}
	if leaf.PublicKey.(*ecdsa.PublicKey).Equal(ca.PublicKey) {
		t.Fatal("the session CA key takes part in TLS handshakes")
	}
}

// A TMPDIR too long for a Unix socket path names the cause instead of EINVAL.
func TestProxyLongTMPDIRFailsClearly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("d", 100))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", dir)
	_, err := startProxy(context.Background(), []destination{{host: "127.0.0.1", port: "1", scheme: "http"}})
	if err == nil || !strings.Contains(err.Error(), "Unix socket limit") {
		t.Fatalf("long TMPDIR: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("proxy directory left behind: %v", entries)
	}
}

func TestOfflinePolicyRejectsEveryDestination(t *testing.T) {
	ds, err := destinations(Policy{Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := startProxy(t.Context(), ds)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("offline child contacted the host") }))
	defer target.Close()
	u, _ := url.Parse("http://proxy")
	transport := &http.Transport{Proxy: http.ProxyURL(u), DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p.socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for _, address := range []string{target.URL, "http://example.invalid/"} {
		response, err := client.Get(address)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("offline status=%d", response.StatusCode)
		}
	}
	if _, err := destinations(Policy{Offline: true, Providers: []string{target.URL}}); err == nil {
		t.Fatal("offline policy admitted endpoints")
	}
	if _, err := destinations(Policy{}); err == nil {
		t.Fatal("ordinary empty policy became valid")
	}
}

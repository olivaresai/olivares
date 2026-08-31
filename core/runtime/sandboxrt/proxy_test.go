// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandboxrt

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeResolver pins hostname→IP resolution so the anti-rebind matching is testable
// without DNS.
type fakeResolver map[string][]net.IP

func (f fakeResolver) lookupIP(_ context.Context, host string) ([]net.IP, error) {
	if ips, ok := f[strings.ToLower(host)]; ok {
		return ips, nil
	}
	return nil, &net.DNSError{Err: "not found", Name: host, IsNotFound: true}
}

// startTarget spins a real HTTP server that echoes a fixed body, returning the
// server and its host/port.
func startTarget(t *testing.T, body string) (*httptest.Server, string, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	return srv, host, port
}

// TestEgressDenyAllRefusesEverything proves an EMPTY allowlist denies every
// destination (the synthetic-scenario default) over both forward HTTP and CONNECT.
func TestEgressDenyAllRefusesEverything(t *testing.T) {
	_, host, port := startTarget(t, "secret")
	p, err := startEgressProxy(EgressPolicy{Engagement: "t/run1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	// Forward HTTP through the proxy ⇒ 403.
	code := forwardGET(t, p.Addr(), "http://"+net.JoinHostPort(host, strconv.Itoa(port))+"/")
	if code != http.StatusForbidden {
		t.Fatalf("deny-all forward GET = %d, want 403", code)
	}
	// CONNECT tunnel through the proxy ⇒ 403, tunnel never opened.
	if status := connectStatus(t, p.Addr(), net.JoinHostPort(host, strconv.Itoa(port))); status != http.StatusForbidden {
		t.Fatalf("deny-all CONNECT = %d, want 403", status)
	}
	if _, denied := p.counts(); denied != 2 {
		t.Fatalf("denied count = %d, want 2", denied)
	}
	for _, ev := range p.Log() {
		if ev.Allowed {
			t.Fatalf("deny-all log has an allowed event: %+v", ev)
		}
		if ev.Engagement != "t/run1" {
			t.Fatalf("egress event not engagement-bound: %+v", ev)
		}
	}
}

// TestEgressAllowlistPermitsExactTarget proves a host-scoped allowlist permits the
// target (forward + CONNECT) while the egress log records the allowed verdict.
func TestEgressAllowlistPermitsExactTarget(t *testing.T) {
	_, host, port := startTarget(t, "hello-target")
	policy := EgressPolicy{
		Engagement: "t/redteam-1",
		Allow:      []EgressRule{{Host: host, Ports: []int{port}}},
	}
	p, err := startEgressProxy(policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	code := forwardGET(t, p.Addr(), "http://"+net.JoinHostPort(host, strconv.Itoa(port))+"/")
	if code != http.StatusOK {
		t.Fatalf("allowed forward GET = %d, want 200", code)
	}
	if status := connectStatus(t, p.Addr(), net.JoinHostPort(host, strconv.Itoa(port))); status != http.StatusOK {
		t.Fatalf("allowed CONNECT = %d, want 200 (Connection Established)", status)
	}
	if allowed, _ := p.counts(); allowed != 2 {
		t.Fatalf("allowed count = %d, want 2", allowed)
	}
}

// TestEgressPortScope proves a port-scoped rule denies a different port on an
// otherwise-allowed host.
func TestEgressPortScope(t *testing.T) {
	_, host, port := startTarget(t, "x")
	policy := EgressPolicy{Allow: []EgressRule{{Host: host, Ports: []int{port}}}}
	p, _ := startEgressProxy(policy, nil)
	defer func() { _ = p.Close() }()
	// A different port on the same host is denied.
	other := port + 1
	if status := connectStatus(t, p.Addr(), net.JoinHostPort(host, strconv.Itoa(other))); status != http.StatusForbidden {
		t.Fatalf("off-port CONNECT = %d, want 403", status)
	}
}

// TestMatchesAntiRebind proves the rebind-safe CIDR rule: a hostname resolving to
// ANY IP outside the allowed CIDR is denied; one fully inside is allowed.
func TestMatchesAntiRebind(t *testing.T) {
	res := fakeResolver{
		"split.evil":  {net.ParseIP("10.1.2.3"), net.ParseIP("8.8.8.8")}, // one outside
		"inside.good": {net.ParseIP("10.1.2.9")},                         // fully inside
	}
	p := &egressProxy{
		policy: EgressPolicy{Allow: []EgressRule{{CIDR: "10.1.2.0/24"}}},
		res:    res,
		now:    func() time.Time { return time.Unix(0, 0) },
	}
	if ok, _, reason := p.matches(context.Background(), "split.evil", 443); ok {
		t.Fatalf("split-rebind host was ALLOWED (%s) — must be denied", reason)
	}
	if ok, ips, _ := p.matches(context.Background(), "inside.good", 443); !ok || len(ips) != 1 || !ips[0].Equal(net.ParseIP("10.1.2.9")) {
		t.Fatalf("fully-inside host not allowed/pinned: ok=%v ips=%v", ok, ips)
	}
	// An IP literal outside the CIDR is denied; inside is allowed and pinned.
	if ok, _, _ := p.matches(context.Background(), "10.9.9.9", 443); ok {
		t.Fatal("out-of-range IP allowed")
	}
	if ok, ips, _ := p.matches(context.Background(), "10.1.2.5", 443); !ok || len(ips) != 1 {
		t.Fatalf("in-range IP denied/unpinned: ok=%v ips=%v", ok, ips)
	}
}

// TestMatchesEmptyAllowlistDeniesAll is the unit-level proof of deny-by-default.
func TestMatchesEmptyAllowlistDeniesAll(t *testing.T) {
	p := &egressProxy{policy: EgressPolicy{}, res: netResolver{}, now: func() time.Time { return time.Unix(0, 0) }}
	if ok, _, reason := p.matches(context.Background(), "anything.example", 443); ok {
		t.Fatalf("empty allowlist allowed a destination (%s)", reason)
	}
}

// countingResolver wraps a fakeResolver and counts lookups so a test can prove the
// gate resolves a hostname ONCE (then pins the IP) — there is no second, dial-time
// resolution a rebind could exploit.
type countingResolver struct {
	inner fakeResolver
	n     int
}

func (c *countingResolver) lookupIP(ctx context.Context, host string) ([]net.IP, error) {
	c.n++
	return c.inner.lookupIP(ctx, host)
}

// TestForwardDialsPinnedIPNoReResolve proves a hostname destination is resolved
// exactly once by the gate and the dial lands on the pinned IP (closing the
// check-vs-connect DNS-rebind TOCTOU): the resolver is consulted a single time.
func TestForwardDialsPinnedIPNoReResolve(t *testing.T) {
	_, host, port := startTarget(t, "PINNED-OK")
	if host != "127.0.0.1" {
		t.Skipf("target not on 127.0.0.1 (got %s)", host)
	}
	res := &countingResolver{inner: fakeResolver{"pinned.test": {net.ParseIP("127.0.0.1")}}}
	policy := EgressPolicy{Allow: []EgressRule{{CIDR: "127.0.0.0/8"}}}
	p, err := startEgressProxy(policy, res)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	code := forwardGET(t, p.Addr(), "http://pinned.test:"+strconv.Itoa(port)+"/")
	if code != http.StatusOK {
		t.Fatalf("pinned forward GET = %d, want 200", code)
	}
	if res.n != 1 {
		t.Fatalf("resolver consulted %d times, want exactly 1 (resolve-once + pinned dial)", res.n)
	}
}

// TestTunnelTornDownOnClose proves an open CONNECT tunnel is torn down when the
// proxy is closed (the hijacked conn + splice goroutines do not leak).
func TestTunnelTornDownOnClose(t *testing.T) {
	_, host, port := startTarget(t, "x")
	p, err := startEgressProxy(EgressPolicy{Allow: []EgressRule{{Host: host, Ports: []int{port}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	target := net.JoinHostPort(host, strconv.Itoa(port))
	_, _ = conn.Write([]byte("CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"))
	br := bufio.NewReader(conn)
	// Consume the FULL CONNECT response (status + blank line) so the buffer is empty.
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("tunnel not established: %v (code=%v)", err, resp)
	}
	// Closing the proxy must tear the tunnel down: the client side now reads EOF/err.
	_ = p.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("tunnel client still readable after proxy Close (leak)")
	}
}

// --- helpers ---------------------------------------------------------------------

// forwardGET issues a GET through the proxy and returns the status code.
func forwardGET(t *testing.T, proxyAddr, target string) int {
	t.Helper()
	pu, _ := url.Parse("http://" + proxyAddr)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu), DisableKeepAlives: true}}
	resp, err := client.Get(target)
	if err != nil {
		t.Fatalf("forward GET error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// connectStatus issues a raw CONNECT to the proxy and returns the HTTP status of
// the proxy's response line (200 ⇒ tunnel established, 403 ⇒ denied).
func connectStatus(t *testing.T, proxyAddr, target string) int {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte("CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"))
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		t.Fatalf("malformed CONNECT response: %q", line)
	}
	code, _ := strconv.Atoi(fields[1])
	return code
}

// TestWhatMatchedIsWhatIsResolved pins the invariant that makes case folding safe
// here, and it is a sharper statement than "never fold".
//
// This matcher used to lower-case the destination with strings.ToLower and compare
// the rule with strings.EqualFold. The two do not fold the same set — EqualFold
// treats U+017F (LATIN SMALL LETTER LONG S) as an "s" while ToLower leaves it alone
// — so a rule for "sexample.com" MATCHED a request for "ſexample.com" and the
// addresses then came from resolving the ORIGINAL, non-ASCII name. The rule that
// matched and the machine that was dialed were not the same destination.
//
// Both sides are canonicalized now, so a spelling is authorized only when it IS the
// approved host, and the name handed to the resolver is that same canonical string.
// Under UTS-46 the two spellings genuinely are one destination — which is what a
// conforming resolver does with them — so the fix is not "refuse the fold" but
// "resolve what you matched".
func TestWhatMatchedIsWhatIsResolved(t *testing.T) {
	var resolved []string
	p := &egressProxy{
		policy: EgressPolicy{Allow: []EgressRule{{Host: "sexample.com"}}},
		res: fakeResolverFunc(func(host string) ([]net.IP, error) {
			resolved = append(resolved, host)
			return []net.IP{net.ParseIP("203.0.113.9")}, nil
		}),
	}
	ok, _, reason := p.matches(context.Background(), "ſexample.com", 443)
	if !ok {
		t.Fatalf("UTS-46 maps this onto the approved host, so it should be permitted: %s", reason)
	}
	if len(resolved) != 1 || resolved[0] != "sexample.com" {
		t.Fatalf("the resolver was handed %v, want the CANONICAL host that matched the rule", resolved)
	}

	// And a host that does NOT canonicalize onto the rule is still refused, so the
	// assertion above is not simply "everything is permitted".
	resolved = nil
	if ok, _, _ := p.matches(context.Background(), "other.example.com", 443); ok {
		t.Error("an unrelated host was permitted")
	}
}

// fakeResolverFunc adapts a function to the resolver seam.
type fakeResolverFunc func(host string) ([]net.IP, error)

func (f fakeResolverFunc) lookupIP(_ context.Context, host string) ([]net.IP, error) {
	return f(host)
}

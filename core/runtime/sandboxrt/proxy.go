// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandboxrt

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// This file is the EGRESS GATE: an out-of-process, deny-by-default forward proxy
// that is the ONLY network path out of an isolated instance (the instance has no
// NIC of its own; its HTTP(S)_PROXY points here). It enforces the job's
// EgressPolicy allowlist — an empty allowlist denies EVERYTHING — and records an
// engagement-bound log of every connection attempt and its verdict.
//
// It is fully self-contained on net/http + net, so it RUNS and is TESTED with
// real sockets (proxy_test.go) — the load-bearing control of (egress
// fail-closed), independent of whether a host has runsc/firecracker.
//
// REBIND-SAFE BY IP PINNING. The allowlist verdict and the actual dial use the
// SAME resolution: matches() resolves the destination ONCE, validates every IP
// against the allowlist, and returns the validated IPs; permit() pins the first
// one and the proxy DIALS THAT IP (never re-resolving the hostname). So a DNS
// rebind between check and connect cannot smuggle a connection to a denied IP,
// and a hostname whose resolution lands ANY IP outside an allowed CIDR is denied
// outright (docs/SECURITY-HARDENING.md: an allowlist is a capability grant, not a destination
// filter). The Host header / TLS SNI the client set still travel end-to-end.

// resolver looks up the IPs of a hostname. It is an interface so a test pins a
// deterministic resolution without DNS (the default uses net.DefaultResolver).
type resolver interface {
	lookupIP(ctx context.Context, host string) ([]net.IP, error)
}

// netResolver is the production resolver over the standard library.
type netResolver struct{}

func (netResolver) lookupIP(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out, nil
}

// maxEgressLog bounds the in-memory engagement log so a misbehaving workload that
// hammers the proxy cannot grow it without limit; older entries past the cap are
// dropped (the count of allowed/denied still drives the attestation).
const maxEgressLog = 4096

// egressProxy is a running deny-by-default forward proxy bound to one job's
// EgressPolicy. It listens on loopback and tunnels CONNECT (HTTPS) and forwards
// absolute-URI HTTP, permitting a destination only on an allowlist hit and dialing
// the validated IP it pinned at check time.
type egressProxy struct {
	policy EgressPolicy
	res    resolver
	dialer *net.Dialer
	ln     net.Listener
	srv    *http.Server
	now    func() time.Time

	mu      sync.Mutex
	log     []EgressEvent
	allowed int
	denied  int
	// conns tracks live tunnel/forward connections so Close() tears them down (a
	// hijacked CONNECT conn is invisible to http.Server.Shutdown).
	conns   map[net.Conn]struct{}
	closing bool
}

// startEgressProxy binds a deny-by-default proxy for the policy on 127.0.0.1:0
// and starts serving. The caller MUST Close it when the run ends (the engine does
// so in a defer). A nil resolver uses the system resolver.
func startEgressProxy(policy EgressPolicy, res resolver) (*egressProxy, error) {
	if res == nil {
		res = netResolver{}
	}
	// Through the product's single admission point. This address is a
	// hard-coded loopback literal, so the admission is a formality today — which
	// is exactly why it goes through the same door as everything else: the
	// invariant "no component opens a socket outside netbind" only holds if the
	// obviously-safe ones obey it too, and it is what stops this literal from
	// quietly becoming configurable later.
	ln, err := netbind.Listen(context.Background(), "tcp", "127.0.0.1:0", netbind.Policy{
		Component: "sandbox-runtime",
		Purpose:   "egress proxy",
		OptIn:     "(none: this listener is loopback by construction)",
	})
	if err != nil {
		return nil, err
	}
	p := &egressProxy{
		policy: policy,
		res:    res,
		dialer: &net.Dialer{Timeout: 10 * time.Second},
		ln:     ln,
		now:    time.Now,
		conns:  map[net.Conn]struct{}{},
	}
	p.srv = &http.Server{
		Handler:           http.HandlerFunc(p.handle),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// Addr is the proxy's loopback address (host:port) the instance points its
// HTTP_PROXY / HTTPS_PROXY at and the controller-side delivery dials through.
func (p *egressProxy) Addr() string { return p.ln.Addr().String() }

// Close stops the proxy, force-closes every live tunnel/forward connection (so the
// splice goroutines unblock and release their sockets), and shuts down the server.
func (p *egressProxy) Close() error {
	p.mu.Lock()
	p.closing = true
	live := make([]net.Conn, 0, len(p.conns))
	for c := range p.conns {
		live = append(live, c)
	}
	p.conns = map[net.Conn]struct{}{}
	p.mu.Unlock()
	for _, c := range live {
		_ = c.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return p.srv.Shutdown(ctx)
}

// Log returns a copy of the engagement-bound connection log.
func (p *egressProxy) Log() []EgressEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]EgressEvent, len(p.log))
	copy(out, p.log)
	return out
}

// counts returns how many connection attempts were allowed / denied.
func (p *egressProxy) counts() (allowed, denied int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.allowed, p.denied
}

// trackConn registers a live connection so Close() can tear it down. If the proxy
// is already closing, the connection is closed immediately (fail-closed).
func (p *egressProxy) trackConn(c net.Conn) {
	p.mu.Lock()
	if p.closing {
		p.mu.Unlock()
		_ = c.Close()
		return
	}
	p.conns[c] = struct{}{}
	p.mu.Unlock()
}

// untrackConn removes a connection from the live set.
func (p *egressProxy) untrackConn(c net.Conn) {
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
}

// handle routes a proxied request: CONNECT tunnels (HTTPS), an absolute-URI GET/
// POST/... is forwarded (plain HTTP). A request with no proxy target is rejected.
func (p *egressProxy) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleForward(w, r)
}

// handleConnect implements an HTTPS tunnel: it checks the allowlist for the
// requested host:port, and only on a hit dials the PINNED, validated IP and
// splices the two connections. A denied destination returns 403 and is logged —
// the tunnel is never opened.
func (p *egressProxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	host, port := splitHostPort(r.Host, 443)
	ok, pinned := p.permit(r.Context(), host, port)
	if !ok {
		http.Error(w, "egress denied", http.StatusForbidden)
		return
	}
	hj, hok := w.(http.Hijacker)
	if !hok {
		http.Error(w, "proxy cannot hijack", http.StatusInternalServerError)
		return
	}
	upstream, err := p.dialer.DialContext(r.Context(), "tcp", net.JoinHostPort(pinned.String(), strconv.Itoa(port)))
	if err != nil {
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	p.tunnel(client, upstream)
}

// handleForward implements a plain-HTTP forwarding proxy for an absolute-URI
// request. It checks the allowlist for the URL host:port and, only on a hit,
// round-trips the request to the PINNED IP (the Host header is preserved) and
// streams the response back.
func (p *egressProxy) handleForward(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || r.URL.Host == "" {
		http.Error(w, "not a proxy request", http.StatusBadRequest)
		return
	}
	host, port := splitHostPort(r.URL.Host, 80)
	ok, pinned := p.permit(r.Context(), host, port)
	if !ok {
		http.Error(w, "egress denied", http.StatusForbidden)
		return
	}
	// Build an outbound request that carries no proxy-only hop headers.
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Connection")
	out.Header.Del("Proxy-Authorization")
	// Dial the PINNED IP regardless of the address the transport derives from the
	// URL — so the connection lands on exactly the IP matches() validated (no
	// re-resolution). The Host header / authority are unchanged.
	pinnedAddr := net.JoinHostPort(pinned.String(), strconv.Itoa(port))
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return p.dialer.DialContext(ctx, network, pinnedAddr)
		},
		DisableKeepAlives: true,
	}
	resp, err := tr.RoundTrip(out)
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// permit decides and LOGS a connection attempt and returns the validated IP to
// dial. It is allowed only on an allowlist hit (deny-by-default, anti-rebind). The
// verdict is recorded with the destination host:port, engagement-bound — never a
// body or header.
func (p *egressProxy) permit(ctx context.Context, host string, port int) (bool, net.IP) {
	allowed, ips, reason := p.matches(ctx, host, port)
	ev := EgressEvent{
		Engagement: p.policy.Engagement, Host: host, Port: port,
		Allowed: allowed, Reason: reason, At: p.now(),
	}
	p.mu.Lock()
	if len(p.log) < maxEgressLog {
		p.log = append(p.log, ev)
	}
	if allowed {
		p.allowed++
	} else {
		p.denied++
	}
	p.mu.Unlock()
	if allowed && len(ips) > 0 {
		return true, ips[0]
	}
	return false, nil
}

// matches applies the deny-by-default allowlist and returns the validated IP set.
// An empty allowlist denies everything. It resolves the destination ONCE; a rule
// matches when the port is permitted AND either the host equals an exact Host
// rule, or EVERY resolved IP is inside an allowed CIDR / equals an allowed Host-IP
// (so a partial rebind is denied). The returned IPs are what the caller MUST dial
// (resolve-once / pin), closing the check-vs-connect TOCTOU.
func (p *egressProxy) matches(ctx context.Context, host string, port int) (bool, []net.IP, string) {
	if p.policy.denyAll() {
		return false, nil, "deny-all (empty allowlist)"
	}
	// CANONICALIZE, and then compare bytes. The pair this replaces — strings.ToLower
	// here and strings.EqualFold on the rule below — do not fold the same set, so the
	// rule that MATCHED and the name that was RESOLVED were different strings:
	// EqualFold treats U+017F as an "s" while ToLower leaves it alone, so a rule for
	// "sexample.com" authorized a connection whose addresses came from resolving
	// "ſexample.com". Canonicalizing first removes the question, and it is the same
	// definition core/egress applies to the other lanes rather than a second one.
	canonical, cerr := egress.CanonicalHost(host)
	if cerr != nil {
		return false, nil, "destination host is not a valid name or address"
	}
	host = canonical
	// Resolve the destination ONCE (an IP literal is its own resolution). The dial
	// will pin one of these IPs, so check-time and connect-time agree.
	ips, err := p.resolveDest(ctx, host)
	if err != nil || len(ips) == 0 {
		return false, nil, "destination did not resolve"
	}
	// 1) Exact host match (hostname or IP literal) — the red-team target case. The
	// operator authorized this host by name; we still pin the resolved IPs so a
	// re-resolution cannot change the dial target.
	for _, rule := range p.policy.Allow {
		ruleHost, rerr := egress.CanonicalHost(rule.Host)
		if rule.Host != "" && rerr == nil && ruleHost == host {
			if portAllowed(rule, port) {
				return true, ips, "exact host"
			}
			return false, nil, "host allowed but port denied"
		}
	}
	// 2) IP / CIDR containment: require EVERY resolved IP to be allowlisted (anti-
	// rebind — a hostname that splits across in/out IPs is denied).
	for _, ip := range ips {
		if !p.ipAllowed(ip, port) {
			return false, nil, "an IP of the destination is outside the allowlist (rebind-safe deny)"
		}
	}
	return true, ips, "all resolved IPs within allowlist"
}

// resolveDest returns the destination's candidate IPs: an IP literal resolves to
// itself; a hostname is looked up via the resolver.
func (p *egressProxy) resolveDest(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	return p.res.lookupIP(ctx, host)
}

// ipAllowed reports whether an IP (on a port) is permitted by some Host-IP or
// CIDR rule.
func (p *egressProxy) ipAllowed(ip net.IP, port int) bool {
	for _, rule := range p.policy.Allow {
		if rule.Host != "" {
			if hip := net.ParseIP(strings.TrimSpace(rule.Host)); hip != nil && hip.Equal(ip) && portAllowed(rule, port) {
				return true
			}
			continue
		}
		if rule.CIDR == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(strings.TrimSpace(rule.CIDR))
		if err != nil {
			continue
		}
		if ipnet.Contains(ip) && portAllowed(rule, port) {
			return true
		}
	}
	return false
}

// portAllowed reports whether a port is permitted by a rule (empty Ports ⇒ any).
func portAllowed(rule EgressRule, port int) bool {
	if len(rule.Ports) == 0 {
		return true
	}
	for _, pr := range rule.Ports {
		if pr == port {
			return true
		}
	}
	return false
}

// splitHostPort parses a "host:port" authority, falling back to defaultPort when
// no port is present. An IPv6 literal is handled by net.SplitHostPort.
func splitHostPort(authority string, defaultPort int) (string, int) {
	authority = strings.TrimSpace(authority)
	h, ps, err := net.SplitHostPort(authority)
	if err != nil {
		return strings.Trim(authority, "[]"), defaultPort
	}
	port, perr := strconv.Atoi(ps)
	if perr != nil {
		port = defaultPort
	}
	return h, port
}

// tunnel splices a CONNECT tunnel bidirectionally and guarantees teardown: when
// EITHER direction ends, BOTH connections are closed (so the peer copy unblocks)
// and both are untracked. Close() force-closing a tracked conn also unblocks both.
func (p *egressProxy) tunnel(client, upstream net.Conn) {
	p.trackConn(client)
	p.trackConn(upstream)
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(upstream, client)
	go cp(client, upstream)
	go func() {
		<-done // first direction to finish tears the whole tunnel down
		_ = client.Close()
		_ = upstream.Close()
		p.untrackConn(client)
		p.untrackConn(upstream)
	}()
}

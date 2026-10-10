// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package egress confines a session's network to its resolved provider endpoints.
package egress

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stripe/smokescreen/pkg/smokescreen"
	acl "github.com/stripe/smokescreen/pkg/smokescreen/acl/v1"
	"github.com/stripe/smokescreen/pkg/smokescreen/conntrack"

	"github.com/olivaresai/olivares/sdk/netbind"
)

// Policy is resolved by the engine, never from a child's proxy environment.
// Controls are existing authenticated HTTP hook/MCP URLs. They grant only
// their exact paths, never a CONNECT tunnel to the engine's API listener.
// PublicOnly admits no loopback or private address for a provider, whatever
// its name resolves to: for administrator-listed hosts, not a bound provider.
type Policy struct {
	Providers  []string
	Controls   []string
	PublicOnly bool
	// Offline retains the same OS network boundary without granting any endpoint.
	// Local session tools such as Git use it even after the agent has stopped.
	Offline bool
}

type destination struct {
	host, port string
	scheme     string
	control    bool
	public     bool
	paths      map[string]bool
}

func (d destination) address() string { return net.JoinHostPort(d.host, d.port) }

func destinations(p Policy) ([]destination, error) {
	if p.Offline {
		if len(p.Providers)+len(p.Controls) != 0 {
			return nil, errors.New("offline session egress cannot grant endpoints")
		}
		return nil, nil
	}
	if len(p.Providers) == 0 || len(p.Providers)+len(p.Controls) > 16 {
		return nil, errors.New("session egress requires a bounded provider endpoint list")
	}
	var out []destination
	for _, group := range []struct {
		urls    []string
		control bool
	}{{p.Providers, false}, {p.Controls, true}} {
		for _, raw := range group.urls {
			u, err := url.Parse(raw)
			if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
				return nil, errors.New("session egress endpoint is invalid")
			}
			host := strings.ToLower(u.Hostname())
			if strings.ContainsAny(host, "*%\\\r\n\x00") {
				return nil, errors.New("session egress endpoint host is invalid")
			}
			port := u.Port()
			if port == "" {
				port = "443"
				if u.Scheme == "http" {
					port = "80"
				}
			}
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return nil, errors.New("session egress endpoint port is invalid")
			}
			d := destination{host: host, port: strconv.Itoa(n), scheme: u.Scheme, control: group.control, public: p.PublicOnly && !group.control}
			if group.control {
				// The owned PEP listener is plaintext loopback; opening TLS CONNECT
				// here would lose the HTTP path boundary.
				ip := net.ParseIP(host)
				if u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
					return nil, errors.New("session control relay requires an HTTP loopback endpoint")
				}
				path := u.EscapedPath()
				if path == "" {
					path = "/"
				}
				if path != "/" && path != "/permission-prompt" && path != "/session/mcp" {
					return nil, errors.New("session control relay path is invalid")
				}
				d.paths = map[string]bool{path: true}
			}
			merged := false
			for i := range out {
				if out[i].address() != d.address() {
					continue
				}
				if out[i].control != d.control || out[i].scheme != d.scheme {
					return nil, errors.New("session provider and control endpoints overlap")
				}
				for path := range d.paths {
					out[i].paths[path] = true
				}
				merged = true
			}
			if !merged {
				out = append(out, d)
			}
		}
	}
	return out, nil
}

type proxy struct {
	dir, socket string
	server      *http.Server
	transport   *http.Transport
	caPEM       []byte
	tlsConfig   *tls.Config
	mu          sync.Mutex
	closed      bool
	connections map[*trackedConn]bool
	done        chan struct{}
}

// Track both accepted sockets and outbound tunnels: net/http does not close
// hijacked CONNECT connections during Server.Close.
type trackedConn struct {
	net.Conn
	owner *proxy
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.owner.mu.Lock()
	delete(c.owner.connections, c)
	c.owner.mu.Unlock()
	return err
}
func (p *proxy) track(c net.Conn) (net.Conn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = c.Close()
		return nil, net.ErrClosed
	}
	t := &trackedConn{Conn: c, owner: p}
	p.connections[t] = true
	return t, nil
}

type trackedListener struct {
	net.Listener
	owner *proxy
}

func (l trackedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return l.owner.track(c)
}

func startProxy(ctx context.Context, ds []destination) (*proxy, error) {
	return startProxyWithResolver(ctx, ds, net.DefaultResolver)
}

// Resolve in the engine namespace once. The proxy never follows later DNS
// changes, and private addresses are granted only for the selected host/port.
type pinnedResolver map[string][]net.IP

func (p pinnedResolver) LookupIP(_ context.Context, _ string, host string) ([]net.IP, error) {
	ips := p[strings.ToLower(host)]
	// Smokescreen rechecks each literal failover address against its own IP
	// classifier. Only addresses pinned at launch can enter that path.
	if ip := net.ParseIP(host); len(ips) == 0 && ip != nil {
		for _, pinned := range p {
			for _, candidate := range pinned {
				if candidate.Equal(ip) {
					return []net.IP{ip}, nil
				}
			}
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("session destination has no pinned address")
	}
	return ips, nil
}

func (p pinnedResolver) LookupPort(ctx context.Context, network, service string) (int, error) {
	return net.DefaultResolver.LookupPort(ctx, network, service)
}

func startProxyWithResolver(ctx context.Context, ds []destination, resolver smokescreen.Resolver) (*proxy, error) {
	return startProxyWithDialer(ctx, ds, resolver, func(ctx context.Context, network, address string, timeout time.Duration) (net.Conn, error) {
		return (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, address)
	})
}

func startProxyWithDialer(ctx context.Context, ds []destination, resolver smokescreen.Resolver, dial func(context.Context, string, string, time.Duration) (net.Conn, error)) (*proxy, error) {
	pins := pinnedResolver{}
	var hosts, local []string
	for _, d := range ds {
		ips, ok := pins[d.host]
		if !ok {
			lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
			var err error
			ips, err = resolver.LookupIP(lookup, "ip", d.host)
			cancel()
			if err != nil || len(ips) == 0 {
				return nil, errors.New("session provider DNS resolution failed")
			}
			// Prefer IPv4 when both are returned, matching local tool defaults.
			sort.SliceStable(ips, func(i, j int) bool { return ips[i].To4() != nil && ips[j].To4() == nil })
			pins[d.host] = ips
		}
		hosts = append(hosts, d.host)
		for _, ip := range ips {
			if !d.public && (ip.IsLoopback() || ip.IsPrivate()) {
				local = append(local, net.JoinHostPort(ip.String(), d.port))
			}
		}
	}
	dir, err := os.MkdirTemp("", "olivares-egress-")
	if err != nil {
		return nil, err
	}
	p := &proxy{dir: dir, socket: filepath.Join(dir, "proxy.sock"), connections: make(map[*trackedConn]bool), done: make(chan struct{})}
	// sun_path holds 107 bytes; past that, bind fails with a bare EINVAL.
	if len(p.socket) > 107 {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("session proxy socket path is %d bytes, over the 107-byte Unix socket limit; give the engine a shorter TMPDIR", len(p.socket))
	}
	l, err := netbind.Listen(ctx, "unix", p.socket, netbind.Policy{Component: "sessions", Purpose: "private egress proxy"})
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := os.Chmod(p.socket, 0o600); err != nil {
		_ = l.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	config := smokescreen.NewConfig()
	config.Resolver = pins
	config.Log.SetOutput(io.Discard) // no URLs, headers or credentials in proxy logs
	config.ConnectTimeout = 5 * time.Second
	config.IdleTimeout = 5 * time.Minute
	config.ShuttingDown.Store(false)
	config.ConnTracker = conntrack.NewTracker(config.IdleTimeout, config.MetricsClient, config.Log, config.ShuttingDown, nil)
	config.RoleFromRequest = func(*http.Request) (string, error) { return "session", nil }
	config.EgressACL = &acl.ACL{Rules: map[string]acl.Rule{"session": {Policy: acl.Enforce, DomainGlobs: hosts}}}
	if err := config.SetAllowAddresses(local); err != nil {
		_ = l.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	config.ProxyDialTimeout = func(call context.Context, network, address string, timeout time.Duration) (net.Conn, error) {
		c, err := dial(call, network, address, timeout)
		if err != nil {
			return nil, err
		}
		return p.track(c)
	}
	p.caPEM, p.tlsConfig, err = sessionTLS(ds)
	if err != nil {
		_ = l.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	config.PostDecisionRequestHandler = func(r *http.Request) error {
		// Keep the authority check at smokescreen's forwarding boundary too;
		// decrypted requests must never reopen an opaque channel.
		if r.Header.Get("X-Upstream-Https-Proxy") != "" || r.Header.Get("Upgrade") != "" ||
			(r.Method == http.MethodConnect && r.URL.Scheme != "") || !requestAllowed(r, ds) {
			return errors.New("session destination denied")
		}
		return nil
	}
	upstream := smokescreen.BuildProxy(config)
	// Reuse smokescreen's checked dial for every candidate; never call the raw
	// dialer for an alternate IP or redo DNS after session creation.
	checkedDial := upstream.Tr.DialContext
	upstream.Tr.DialContext = func(call context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips := pins[strings.ToLower(host)]
		if len(ips) == 0 {
			return nil, errors.New("session destination has no pinned address")
		}
		var last error
		for _, ip := range ips {
			attempt, cancel := context.WithTimeout(call, config.ConnectTimeout/time.Duration(len(ips)))
			c, err := checkedDial(attempt, network, net.JoinHostPort(ip.String(), port))
			cancel()
			if err == nil {
				return c, nil
			}
			last = err
			if call.Err() != nil {
				break
			}
		}
		return nil, last
	}
	upstream.Logger = log.New(io.Discard, "", 0)
	upstream.Tr.Proxy = nil
	upstream.ConnectDial = nil
	upstream.Tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	p.transport = upstream.Tr
	p.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, ErrorLog: log.New(io.Discard, "", 0),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Never honor a client-selected upstream proxy or permit Host/URL
			// disagreement to choose a second destination.
			if r.Header.Get("X-Upstream-Https-Proxy") != "" || r.Header.Get("Upgrade") != "" || !requestAllowed(r, ds) {
				http.Error(w, "session destination denied", http.StatusForbidden)
				return
			}
			if r.Method == http.MethodConnect {
				p.serveConnect(w, r, p.server.Handler)
				return
			}
			upstream.ServeHTTP(streamingWriter{w}, r)
		}),
	}
	go func() { defer close(p.done); _ = p.server.Serve(trackedListener{l, p}) }()
	go func() {
		select {
		case <-ctx.Done():
			p.close()
		case <-p.done:
		}
	}()
	return p, nil
}

// goproxy copies HTTP response bodies without flushing; model and MCP event
// streams must reach the client before the upstream closes the response.
type streamingWriter struct{ http.ResponseWriter }

func (w streamingWriter) WriteHeader(status int) {
	w.ResponseWriter.WriteHeader(status)
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func (w streamingWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	if err == nil {
		err = http.NewResponseController(w.ResponseWriter).Flush()
	}
	return n, err
}

func requestAllowed(r *http.Request, ds []destination) bool {
	u := r.URL
	if r.Method == http.MethodConnect {
		u = &url.URL{Scheme: "https", Host: r.Host}
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	for _, d := range ds {
		if !strings.EqualFold(u.Hostname(), d.host) || port != d.port {
			continue
		}
		authority := &url.URL{Scheme: u.Scheme, Host: r.Host}
		authorityPort := authority.Port()
		if authorityPort == "" {
			authorityPort = "80"
			if u.Scheme == "https" {
				authorityPort = "443"
			}
		}
		if !strings.EqualFold(authority.Hostname(), d.host) || authorityPort != d.port {
			return false
		}
		if !d.control {
			if r.Method == http.MethodConnect {
				return d.scheme == "https"
			}
			return u.Scheme == d.scheme
		}
		return r.Method != http.MethodConnect && u.Scheme == "http" && d.paths[u.EscapedPath()] &&
			(r.Method == "GET" || r.Method == "POST" || r.Method == "DELETE") &&
			strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	return false
}

func (p *proxy) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	var cs []*trackedConn
	for c := range p.connections {
		cs = append(cs, c)
	}
	p.mu.Unlock()
	_ = p.server.Close()
	p.transport.CloseIdleConnections()
	for _, c := range cs {
		_ = c.Close()
	}
	<-p.done
	_ = os.RemoveAll(p.dir)
}

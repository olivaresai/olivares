// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// exposure & access controls for the Prometheus /metrics endpoint.
//
// By default the endpoint is unauthenticated and bound to the trusted scrape
// network (the engine's own listen address; the Helm NetworkPolicy or a firewall
// restrict who can reach it). An operator may layer application-level access
// control on top: a static bearer token, a CIDR allowlist, or both.
//
// When BOTH are configured, a request must satisfy BOTH (AND logic): the peer IP
// must be inside an allowed CIDR AND the bearer token must match. This is the
// defense-in-depth posture: the CIDR catches the "wrong network" case and the
// token catches the "right network, wrong pod" case.

// MetricsConfig configures application-level access control on /metrics. The zero
// value (or a nil *MetricsConfig in Options) leaves the endpoint unauthenticated
// — the historical default, still the safe posture when the scrape network is
// controlled by infrastructure (NetworkPolicy, firewall, bind address).
type MetricsConfig struct {
	// Token, when non-empty, requires a matching Authorization: Bearer <token> on
	// every scrape request. The comparison is constant-time. Prometheus supports
	// bearer_token / bearer_token_file in its scrape_config.
	Token string
	// AllowedCIDRs, when non-empty, confines the endpoint to clients whose DIRECT
	// PEER address is inside one of these CIDRs. A request from outside gets 403.
	// Uses the direct peer on purpose (X-Forwarded-For is client-settable).
	AllowedCIDRs []string
}

// metricsGate is the parsed, request-hot-path form of MetricsConfig. A nil gate
// means "unauthenticated, no restriction".
type metricsGate struct {
	token []byte
	cidrs []*net.IPNet
}

// buildMetricsGate compiles a MetricsConfig into a gate, failing at build time on
// a malformed CIDR (an operator deserves the error up front, not silent open
// exposure).
func buildMetricsGate(cfg *MetricsConfig) (*metricsGate, error) {
	if cfg == nil {
		return nil, nil
	}
	if cfg.Token == "" && len(cfg.AllowedCIDRs) == 0 {
		return nil, nil
	}
	g := &metricsGate{}
	if cfg.Token != "" {
		g.token = []byte(cfg.Token)
	}
	for _, c := range cfg.AllowedCIDRs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("api: MetricsConfig.AllowedCIDRs: invalid CIDR %q: %w", c, err)
		}
		g.cidrs = append(g.cidrs, n)
	}
	return g, nil
}

// allowMetrics enforces the metrics access control for a scrape request. It writes
// the response and returns false when blocked. A nil gate always allows.
func (s *Server) allowMetrics(w http.ResponseWriter, r *http.Request) bool {
	g := s.metricsGate
	if g == nil {
		return true
	}
	if len(g.cidrs) > 0 && !g.metricsPeerAllowed(r.RemoteAddr) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	if len(g.token) > 0 {
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return false
		}
		presented := []byte(auth[len(prefix):])
		if subtle.ConstantTimeCompare(presented, g.token) != 1 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return false
		}
	}
	return true
}

// metricsPeerAllowed reports whether the request's direct peer IP is inside an
// allowed CIDR.
func (g *metricsGate) metricsPeerAllowed(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false // unparseable peer → deny-closed
	}
	for _, n := range g.cidrs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package readyzprobe implements the local, dependency-free readiness probe used
// by the distroless container image. It deliberately accepts loopback origins
// only: this is a probe of the process beside it, not a general HTTP client.
package readyzprobe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Outcome is the three-answer readiness contract exposed by `olivares readyz`.
type Outcome uint8

const (
	// Unmeasurable means the probe could not obtain an HTTP verdict.
	Unmeasurable Outcome = iota
	// Ready means the local /readyz endpoint returned HTTP 200.
	Ready
	// NotReady means the endpoint answered, but with a non-200 status.
	NotReady
)

// Config contains only local probe inputs. Origin must be an HTTP(S) loopback
// origin with no path, query, fragment or credentials.
type Config struct {
	Origin  string
	CACert  string
	Timeout time.Duration
}

// Result records the measured endpoint and status without retaining a body.
// Diagnosis and Remedy are optional: they are set only when a not-ready answer
// carried a readiness state this package recognizes, and they are always
// sentences stored in this binary, never text taken from the response.
type Result struct {
	Outcome    Outcome
	Endpoint   string
	StatusCode int
	Diagnosis  string
	Remedy     string
}

// Check asks the local process's /readyz endpoint for its verdict. HTTP 200 is
// the only ready answer; every other received status is measured not-ready.
// Transport, trust-anchor and input failures are unmeasurable.
func Check(ctx context.Context, cfg Config) (Result, error) {
	result := Result{Outcome: Unmeasurable}
	endpoint, scheme, err := localEndpoint(cfg.Origin)
	if err != nil {
		return result, err
	}
	result.Endpoint = endpoint
	if cfg.Timeout <= 0 {
		return result, errors.New("--timeout must be positive")
	}
	if scheme != "https" && cfg.CACert != "" {
		return result, errors.New("--ca-cert requires an https --server")
	}

	transport := &http.Transport{
		Proxy:             nil, // a loopback health probe never belongs in an ambient proxy
		DisableKeepAlives: true,
		DialContext: (&net.Dialer{
			Timeout: cfg.Timeout,
		}).DialContext,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	if scheme == "https" && cfg.CACert != "" {
		roots, rerr := x509.SystemCertPool()
		if rerr != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		pemBytes, rerr := os.ReadFile(cfg.CACert)
		if rerr != nil {
			return result, fmt.Errorf("read --ca-cert: %w", rerr)
		}
		if !roots.AppendCertsFromPEM(pemBytes) {
			return result, errors.New("--ca-cert contains no PEM certificate")
		}
		transport.TLSClientConfig.RootCAs = roots
	}

	probeCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return result, fmt.Errorf("build local readiness request: %w", err)
	}
	// cli-transport-exempt: the distroless healthcheck contacts numeric loopback only,
	// carries no operator credential and must ignore control-plane endpoints, headers
	// and ambient proxies; inheriting cliTransport would violate that local-only contract.
	client := &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("GET %s: %w", endpoint, err)
	}
	defer func() { _ = response.Body.Close() }()
	result.StatusCode = response.StatusCode
	if response.StatusCode == http.StatusOK {
		result.Outcome = Ready
		return result, nil
	}
	// The status has already decided the verdict. Reading the body can only add
	// a fixed local sentence to a not-ready answer; it can never make one ready,
	// and a body that is absent, malformed, oversized or truncated simply adds
	// nothing. The read shares the probe deadline set above and is bounded by
	// maxDiagnosticBody, so a slow or endless body cannot hold the probe open.
	result.Outcome = NotReady
	result.Diagnosis, result.Remedy = diagnose(response.Body)
	return result, nil
}

func localEndpoint(rawOrigin string) (endpoint, scheme string, err error) {
	u, err := url.Parse(rawOrigin)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return "", "", errors.New("--server must be a loopback http(s) origin")
	}
	scheme = strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", "", errors.New("--server must use http or https")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawPath != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", "", errors.New("--server must not contain credentials, path, query or fragment")
	}
	host := net.ParseIP(u.Hostname())
	if host == nil || !host.IsLoopback() {
		return "", "", errors.New("--server must name a numeric loopback address")
	}
	u.Path = "/readyz"
	return u.String(), scheme, nil
}

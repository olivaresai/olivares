// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// This file holds the minimal, stdlib-only HTTP clients the imperative backends
// use to WRITE to a runtime API. They MIRROR the least-privilege client mechanics
// of connectors/runtime — a unix-socket transport for the Docker daemon, a
// TLS 1.2+ bearer client for the Kubernetes/Nomad API — but the read-only
// connector's clients are unexported, and a connector must never import /core, so
// the WRITE-capable mechanics live HERE in the AGPL motor (docs/SECURITY-HARDENING.md; the brief
// Task 6/15). No third-party dependency is added: net/http + crypto/tls only.
//
// SECURITY: a bearer credential is set ONLY in the Authorization header (never in a
// URL, a log, or an error). Response bodies are size-bounded. Transport errors are
// scrubbed so a host/token can never leak into a surfaced error (docs/SECURITY-HARDENING.md).

// default response-body caps, mirroring connectors/runtime.
const (
	maxDockerBody = 8 << 20  // 8 MiB
	maxAPIBody    = 32 << 20 // 32 MiB
)

// unixHTTPClient builds an HTTP client whose transport dials a unix socket (the
// Docker daemon). The host in request URLs is a placeholder; the socket carries it.
func unixHTTPClient(socketPath string, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

// tlsBearerClient builds an HTTPS client for an API server (Kubernetes, Nomad,
// Crossplane via the K8s API). TLS 1.2 is the floor with no fallback (docs/SECURITY-HARDENING.md,
// §3,§4). A CA bundle pins the server; insecure-skip is an explicit operator
// opt-in only (never the default).
func tlsBearerClient(caPEM []byte, insecure bool, timeout time.Duration) (*http.Client, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch {
	case insecure:
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // explicit operator opt-in, never default
	case len(caPEM) > 0:
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("executor: API server CA bundle is not valid PEM")
		}
		tlsCfg.RootCAs = pool
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}, nil
}

// apiRequest is one HTTP call to a runtime API.
type apiRequest struct {
	method      string
	baseURL     string // scheme+host (or the docker placeholder "http://docker")
	path        string // path + query
	bearer      string // Authorization: Bearer <bearer> — header only, never logged
	body        []byte
	contentType string
	accept      string
	// headers carries extra request headers (e.g. Nomad's X-Nomad-Token, a
	// fieldManager). Values are header-only and never logged; do not place a secret
	// here unless it is a credential header (it is set on the wire, never recorded).
	headers map[string]string
}

// doAPI issues one request and returns the status code and (size-bounded) body.
// The error is always SCRUBBED of any URL/token before return.
func doAPI(ctx context.Context, client *http.Client, req apiRequest, maxBody int64) (int, []byte, error) {
	var rdr io.Reader = http.NoBody
	if len(req.body) > 0 {
		rdr = bytes.NewReader(req.body)
	}
	hr, err := http.NewRequestWithContext(ctx, req.method, req.baseURL+req.path, rdr)
	if err != nil {
		return 0, nil, errors.New("executor: malformed runtime API request")
	}
	if req.bearer != "" {
		hr.Header.Set("Authorization", "Bearer "+req.bearer)
	}
	if req.contentType != "" {
		hr.Header.Set("Content-Type", req.contentType)
	}
	if req.accept != "" {
		hr.Header.Set("Accept", req.accept)
	}
	for k, v := range req.headers {
		hr.Header.Set(k, v)
	}
	resp, err := client.Do(hr)
	if err != nil {
		return 0, nil, fmt.Errorf("executor: runtime API call failed (%s): %w", req.method, scrubTransportErr(err))
	}
	defer resp.Body.Close()
	if maxBody <= 0 {
		maxBody = maxAPIBody
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, nil, errors.New("executor: runtime API response read failed")
	}
	return resp.StatusCode, body, nil
}

// scrubTransportErr removes any URL/credential text from a transport error so a
// host or token can never reach a surfaced 502 body or a log (docs/SECURITY-HARDENING.md). It keeps
// only the coarse failure class.
func scrubTransportErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("timeout")
	case errors.Is(err, context.Canceled):
		return errors.New("canceled")
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return errors.New("connection refused")
	case strings.Contains(msg, "no such host"):
		return errors.New("host unresolved")
	case strings.Contains(msg, "tls"), strings.Contains(msg, "certificate"), strings.Contains(msg, "x509"):
		return errors.New("TLS handshake failed")
	default:
		return errors.New("transport error")
	}
}

// ok2xx reports whether a status code is a 2xx success.
func ok2xx(code int) bool { return code >= 200 && code < 300 }

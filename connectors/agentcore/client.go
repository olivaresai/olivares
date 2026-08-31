// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package agentcore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/awssig"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

// signingService is the SigV4 signing name. TRAP (VERIFIED 2026-06-11 from the
// botocore service model): the service's endpointPrefix is
// "bedrock-agentcore-control" — that is the HOSTNAME — but its signingName is
// "bedrock-agentcore". Signing with the endpoint prefix produces a
// credential-scope mismatch and every call is rejected. Never derive the
// signing name from the host.
const signingService = "bedrock-agentcore"

// maxBodyBytes bounds a successful JSON response (a control-plane page is
// small; this protects memory against a hostile or runaway endpoint).
const maxBodyBytes = 16 << 20 // 16 MiB

// maxErrExcerpt bounds how much of an error response body is surfaced for
// diagnostics.
const maxErrExcerpt = 2 << 10 // 2 KiB

// apiError is the typed non-2xx error: it carries the operation, the status and
// a bounded excerpt of the service's error body — never the credential (the
// credential exists only in the signing headers, which are never echoed here).
type apiError struct {
	op      string
	status  int
	excerpt string
}

// Error renders "agentcore: <METHOD path>: status <code>: <excerpt>".
func (e *apiError) Error() string {
	return fmt.Sprintf("agentcore: %s: status %d: %s", e.op, e.status, e.excerpt)
}

// isStatus reports whether err is an *apiError with one of the given statuses
// (the per-identity degrade discrimination in detail mode).
func isStatus(err error, statuses ...int) bool {
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	for _, s := range statuses {
		if ae.status == s {
			return true
		}
	}
	return false
}

// client is the package-local signed transport for bedrock-agentcore-control.
// This API is NOT GET-only, so the shared httpx.Client cannot serve it: the
// identity operations are RPC-style POSTs under /identities/<OperationName>
// with a JSON body, while the policy operations are REST GETs. Every operation
// this connector invokes is a READ (List*/Get*) regardless of the HTTP verb —
// the POST is the protocol's RPC envelope, not a mutation. Each request is
// SigV4-signed in place (host;x-amz-date[;x-amz-security-token] — awssig
// mutates only the signing headers, never the URL or body).
type client struct {
	endpoint string
	region   string
	creds    awssig.Creds
	doer     httpx.Doer
	now      func() time.Time
	timeout  time.Duration
}

// newClient builds the signed transport from the Source's resolved config.
func (s *Source) newClient() *client {
	doer := s.doer
	if doer == nil {
		doer = http.DefaultClient
	}
	return &client{
		endpoint: s.endpoint,
		region:   s.region,
		creds:    s.creds,
		doer:     doer,
		now:      s.clock,
		timeout:  s.timeout,
	}
}

// postJSON issues one RPC-style identity read: POST /identities/<op> with a
// JSON body (Content-Type: application/json), and decodes the JSON response
// into out.
func (c *client) postJSON(ctx context.Context, op string, in, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("agentcore: encode %s request: %w", op, err)
	}
	return c.do(ctx, http.MethodPost, "/identities/"+op, nil, raw, out)
}

// getJSON issues one REST policy read: GET path?query, decoding into out.
func (c *client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

// do builds, signs and issues one request. The response decode is bounded
// (maxBodyBytes); a non-2xx becomes a typed *apiError carrying the status and a
// bounded body excerpt, never the credential.
func (c *client) do(ctx context.Context, method, path string, query url.Values, body []byte, out any) error {
	u := c.endpoint + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return fmt.Errorf("agentcore: build %s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	// Sign in place with the bedrock-agentcore signing name (see signingService).
	// A GET signs the empty-payload hash (body == nil).
	awssig.Sign(req, body, signingService, c.region, c.creds, c.now())

	resp, err := c.doer.Do(req)
	if err != nil {
		return fmt.Errorf("agentcore: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrExcerpt))
		// The excerpt is the service's error body; the request (whose signing
		// headers hold the credential) is never included.
		return &apiError{op: method + " " + path, status: resp.StatusCode, excerpt: strings.TrimSpace(string(excerpt))}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(out); err != nil {
		return fmt.Errorf("agentcore: decode %s %s: %w", method, path, err)
	}
	return nil
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"syscall"
)

// ErrStreamableRedirectRefused reports that a configured Streamable HTTP MCP
// endpoint answered with a redirect (301, 302, 303, 307 or 308 with a Location)
// and the transport did not follow it. The HTTP transports send requests only to
// the endpoint the operator configured, so a server that answers from another
// address must be configured with that final URL. Every error the transports
// return for a refused redirect matches it with errors.Is, and none of them
// carries the Location or the response body.
var ErrStreamableRedirectRefused = errors.New("mcp: streamable http endpoint answered with a redirect, which is not followed; configure the final endpoint URL")

// newStreamableClient returns the HTTP client of the Streamable HTTP transports.
// base keeps every connection concern: production passes http.DefaultTransport,
// so proxy settings from the environment, connection reuse, HTTP/2 and TLS are
// the ones the transports always had. Only redirects change: they are refused
// before net/http reads a Location.
func newStreamableClient(base http.RoundTripper) *http.Client {
	return &http.Client{
		Transport: refuseRedirects{base: base},
		// refuseRedirects already stops every redirect the client would follow;
		// the policy refuses too, so no path can follow a Location it misses.
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrStreamableRedirectRefused },
	}
}

// refuseRedirects passes every request to base and every response back as it
// is, except a response the client would follow as a redirect: that one is
// closed and replaced by ErrStreamableRedirectRefused. It runs before the client
// parses the Location, so neither a valid nor a malformed Location reaches an
// error or a second request. The body is closed without being read, because a
// server can keep it open indefinitely; the connection is then not reused.
type refuseRedirects struct{ base http.RoundTripper }

// RoundTrip implements http.RoundTripper.
func (r refuseRedirects) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if followedRedirect(resp.StatusCode) && resp.Header.Get("Location") != "" {
		_ = resp.Body.Close()
		return nil, ErrStreamableRedirectRefused
	}
	return resp, nil
}

// followedRedirect reports the statuses net/http's Client follows when the
// response has a Location. Any other response, including a redirect status
// without a Location, reaches the transport as before.
func followedRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// streamableRequestError is a Streamable HTTP request that could not be built,
// sent or answered. Its text is op and a reason from a fixed list, never the
// cause's own text: the *url.Error net/http returns quotes the request URL with
// its path, query and user name, and a cause below it can quote hosts, proxies
// or a Location. Unwrap returns the cause, so errors.Is and errors.As still reach
// context.Canceled, a *net.OpError or ErrStreamableRedirectRefused.
type streamableRequestError struct {
	op    string
	cause error
}

func (e *streamableRequestError) Error() string {
	return e.op + ": " + streamableFailureReason(e.cause)
}

func (e *streamableRequestError) Unwrap() error { return e.cause }

// streamableFailureReason names the class of a request failure without quoting
// the failure.
func streamableFailureReason(err error) string {
	var (
		urlErr  *url.Error
		dnsErr  *net.DNSError
		certErr *tls.CertificateVerificationError
	)
	switch {
	case errors.Is(err, ErrStreamableRedirectRefused):
		return "redirect refused; configure the final endpoint URL"
	case errors.Is(err, context.Canceled):
		return "context canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline exceeded"
	case errors.As(err, &urlErr) && urlErr.Op == "parse":
		return "invalid endpoint URL"
	case errors.As(err, &dnsErr):
		return "host name resolution failed"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.As(err, &certErr):
		return "server certificate verification failed"
	default:
		return "request failed"
	}
}

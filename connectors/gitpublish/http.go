// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"syscall"
	"time"
)

// Doer is the HTTP capability the adapter needs. *http.Client from
// NewHTTPClient satisfies it; tests route it to a local fake.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// defaultHosts are the public API hosts accepted without operator allowlisting.
var defaultHosts = []string{"api.github.com", "gitlab.com"}

// ValidateEndpoint applies the endpoint trust rules to an API base: https, no
// userinfo, query or fragment, no IP literal, no dot segments, the default port,
// and a host that is a public default or on the operator's allowlist.
func ValidateEndpoint(raw string, allowed []string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: unparsable", ErrEndpoint)
	}
	if u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("%w: scheme, userinfo, query or fragment", ErrEndpoint)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || net.ParseIP(host) != nil {
		return fmt.Errorf("%w: host must be a DNS name", ErrEndpoint)
	}
	if p := u.Port(); p != "" && p != "443" {
		return fmt.Errorf("%w: port", ErrEndpoint)
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == ".." || seg == "." {
			return fmt.Errorf("%w: dot segment", ErrEndpoint)
		}
	}
	for _, h := range append(append([]string(nil), defaultHosts...), allowed...) {
		if host == strings.ToLower(strings.TrimSpace(h)) {
			return nil
		}
	}
	return fmt.Errorf("%w: host not allowlisted", ErrEndpoint)
}

var errRedirectRefused = errors.New("gitpublish: redirects are refused")

// NewHTTPClient returns the adapter's client: no redirects, no proxy from the
// environment, and no connection to loopback, link-local, unspecified or
// multicast addresses.
func NewHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: refusePeer}
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirectRefused },
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			ForceAttemptHTTP2:   true,
		},
	}
}

func refusePeer(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("%w: peer address", ErrEndpoint)
	}
	return nil
}

// maxBody bounds any host response body the adapter decodes.
const maxBody = 4 << 20

// call is one prepared request and its response.
type call struct {
	status int
	header http.Header
	body   []byte
}

func do(ctx context.Context, d Doer, method, rawURL string, headers map[string]string, in any) (call, error) {
	var rdr io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return call{}, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return call{}, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.Do(req)
	if err != nil {
		// Never wrap the transport error: it can quote the URL and headers.
		return call{}, errTransport
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return call{status: resp.StatusCode, header: resp.Header}, errTransport
	}
	return call{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

var errTransport = errors.New("gitpublish: transport failure")

// sameOrigin reports whether next has the scheme, host and port of base.
func sameOrigin(base, next string) bool {
	b, err1 := url.Parse(base)
	n, err2 := url.Parse(next)
	if err1 != nil || err2 != nil {
		return false
	}
	return b.Scheme == n.Scheme && strings.EqualFold(b.Host, n.Host)
}

// nextLink extracts rel="next" from a Link header.
func nextLink(h http.Header) string {
	for _, part := range strings.Split(h.Get("Link"), ",") {
		part = strings.TrimSpace(part)
		if strings.HasSuffix(part, `rel="next"`) {
			if i, j := strings.Index(part, "<"), strings.Index(part, ">"); i >= 0 && j > i {
				return part[i+1 : j]
			}
		}
	}
	return ""
}

// escapeRef escapes each segment of a branch name for a URL path.
func escapeRef(ref string) string {
	segs := strings.Split(ref, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return path.Join(segs...)
}

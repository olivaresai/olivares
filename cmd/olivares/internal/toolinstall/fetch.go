// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const userAgent = "olivares-tool-install"

// NewHTTPClient returns the transport the installers use: the platform's default
// TLS verification, a header timeout, and a refusal to follow a redirect that
// downgrades https to http. There is no overall client timeout; the artifact
// download is bounded by the caller's context and by the manifest's size.
func NewHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 60 * time.Second
	tr.TLSHandshakeTimeout = 30 * time.Second
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return fmt.Errorf("refusing redirect from https to %s://%s", req.URL.Scheme, req.URL.Host)
			}
			return nil
		},
	}
}

type fetcher struct {
	client *http.Client
}

func (f fetcher) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, refuse(KindTransport, "build request for %s: %v", u, err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, refuse(KindTransport, "GET %s: %v", u, err)
	}
	return resp, nil
}

// small fetches a metadata document of at most limit bytes. A body that exceeds
// the limit, or a Content-Length that announces it will, is refused unread.
func (f fetcher) small(ctx context.Context, u string, limit int64) ([]byte, int, error) {
	resp, err := f.get(ctx, u)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, resp.StatusCode, nil
	}
	if resp.ContentLength > limit {
		return nil, resp.StatusCode, refuse(KindResponseTooLarge, "%s announces %d bytes; this document is capped at %d", u, resp.ContentLength, limit)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, resp.StatusCode, refuse(KindTransport, "read %s: %v", u, err)
	}
	if int64(len(body)) > limit {
		return nil, resp.StatusCode, refuse(KindResponseTooLarge, "%s returned more than %d bytes", u, limit)
	}
	return body, resp.StatusCode, nil
}

// exact streams u into w and requires the body to be exactly want.Size bytes with
// SHA-256 want.SHA256. The size is enforced while reading, so an oversized body
// is cut at want.Size+1 rather than written to disk in full. The caller keeps
// the destination unexecutable until this returns without error.
func (f fetcher) exact(ctx context.Context, u string, want Artifact, w io.Writer) (Artifact, error) {
	resp, err := f.get(ctx, u)
	if err != nil {
		return Artifact{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return Artifact{}, refuse(KindTransport, "GET %s: HTTP %d", u, resp.StatusCode)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != want.Size {
		return Artifact{}, refuse(KindSizeMismatch, "%s announces %d bytes; the verified manifest binds %d", u, resp.ContentLength, want.Size)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, want.Size+1))
	if err != nil {
		return Artifact{}, refuse(KindTransport, "download %s: %v after %d bytes", u, err, n)
	}
	if n != want.Size {
		return Artifact{}, refuse(KindSizeMismatch, "%s delivered %d bytes; the verified manifest binds %d", u, n, want.Size)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want.SHA256 {
		return Artifact{}, refuse(KindDigestMismatch, "%s: sha256 %s does not match the verified manifest's %s", u, got, want.SHA256)
	}
	return Artifact{Name: want.Name, SHA256: got, Size: n}, nil
}

// baseURL validates an operator-supplied source and returns it without a
// trailing slash.
func baseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", refuse(KindUnsupportedSource, "source %q is not an absolute http(s) URL", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", refuse(KindUnsupportedSource, "source %q must not carry a query or fragment", raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

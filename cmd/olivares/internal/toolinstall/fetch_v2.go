// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func (f fetcher) confine(policy RedirectPolicyV2, allowed []string) fetcher {
	client := f.client
	if client == nil {
		client = NewHTTPClient()
	}
	clone := *client
	allowedSet := map[string]struct{}{}
	for _, o := range allowed {
		allowedSet[o] = struct{}{}
	}
	for _, o := range policy.AllowedOrigins {
		allowedSet[o] = struct{}{}
	}
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if policy.MaxHops == 0 || len(via) > policy.MaxHops {
			return refuse(KindTransport, "redirect not permitted by the approved plan (max_hops %d)", policy.MaxHops)
		}
		if req.URL == nil || req.URL.Scheme != "https" {
			return refuse(KindTransport, "refusing a non-https redirect")
		}
		origin := req.URL.Scheme + "://" + strings.ToLower(req.URL.Host)
		if _, ok := allowedSet[origin]; !ok {
			return refuse(KindTransport, "redirect origin %q is not in the approved plan", origin)
		}
		return nil
	}
	return fetcher{client: &clone}
}

func (f fetcher) bounded(ctx context.Context, u string, max int64, w io.Writer) (FetchedObjectObserved, error) {
	if max <= 0 || max > maxV2FetchedBytes {
		return FetchedObjectObserved{}, refuse(KindInvalidRequest, "fetched object max_size %d is outside 1..%d", max, maxV2FetchedBytes)
	}
	resp, err := f.get(ctx, u)
	if err != nil {
		return FetchedObjectObserved{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return FetchedObjectObserved{}, refuse(KindTransport, "GET %s: HTTP %d", u, resp.StatusCode)
	}
	if resp.ContentLength > max {
		return FetchedObjectObserved{}, refuse(KindResponseTooLarge, "%s announces %d bytes; this object is capped at %d", u, resp.ContentLength, max)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, max+1))
	if err != nil {
		return FetchedObjectObserved{}, refuse(KindTransport, "download %s: %v after %d bytes", u, err, n)
	}
	if n > max {
		return FetchedObjectObserved{}, refuse(KindResponseTooLarge, "%s delivered more than %d bytes", u, max)
	}
	if n <= 0 {
		return FetchedObjectObserved{}, refuse(KindSizeMismatch, "%s delivered no bytes", u)
	}
	return FetchedObjectObserved{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

func (f fetcher) exactSizeHash(ctx context.Context, u string, wantSHA string, wantSize, max int64, w io.Writer) (FetchedObjectObserved, error) {
	if wantSize <= 0 || wantSize > max {
		return FetchedObjectObserved{}, refuse(KindInvalidRequest, "fetched object size %d is outside 1..max_size %d", wantSize, max)
	}
	if !isHex64(wantSHA) {
		return FetchedObjectObserved{}, refuse(KindInvalidRequest, "fetched object sha256 is not a lowercase hex-64")
	}
	got, err := f.bounded(ctx, u, wantSize, w)
	if err != nil {
		return FetchedObjectObserved{}, err
	}
	if got.Size != wantSize {
		return FetchedObjectObserved{}, refuse(KindSizeMismatch, "%s delivered %d bytes; the plan binds %d", u, got.Size, wantSize)
	}
	if got.SHA256 != wantSHA {
		return FetchedObjectObserved{}, refuse(KindDigestMismatch, "%s: sha256 %s does not match the plan's %s", u, got.SHA256, wantSHA)
	}
	return got, nil
}

func originOfBase(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", refuse(KindUnsupportedSource, "base %q has no https origin", base)
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

func joinURL(base, extra string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(extra, "/")
}

func statusOrTransport(status int, u string, kind string) error {
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusNotFound, http.StatusForbidden:
		if kind == "version" {
			return refuse(KindVersionUnknown, "%s answered HTTP %d", u, status)
		}
		return refuse(KindUnsupportedSource, "%s answered HTTP %d", u, status)
	default:
		return refuse(KindTransport, "GET %s: HTTP %d", u, status)
	}
}

func requirePresentOrigin(raw, what string, allowed string) error {
	if err := requireHTTPSURL(raw, what); err != nil {
		return err
	}
	origin, err := canonicalOrigin(raw)
	if err != nil {
		return err
	}
	if origin != allowed {
		return refuse(KindInvalidRequest, "%s origin %q is not %s", what, origin, allowed)
	}
	return nil
}

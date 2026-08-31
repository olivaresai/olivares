// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// Notice describes one deprecated control-plane endpoint observed on the wire,
// per the API stability policy: the engine marks a deprecated route with an
// RFC 9745 `Deprecation` header, optionally an RFC 8594 `Sunset` header, and
// `Link` headers pointing at the migration guide.
//
// Header values are kept verbatim (no parsing into time.Time): the provider's
// job is to relay the server's signal into a log line, and a raw value can
// never fail to "parse" mid-run — the policy page documents the formats.
type Notice struct {
	// Method and Path identify the deprecated route as it was called
	// (path only — no host, no query), e.g. GET + /v1/agents/agt_001.
	Method string
	Path   string
	// Deprecation is the raw RFC 9745 header value, a Structured Field Date
	// such as "@1782864000" (the moment the deprecation was announced).
	Deprecation string
	// Sunset is the raw RFC 8594 header value, an HTTP-date after which the
	// route may stop working. Empty when the server announced a deprecation
	// without committing to a retirement date yet.
	Sunset string
	// Link is the target of the Link header with rel="deprecation" — the
	// human-readable migration guide. Empty when the server sent none.
	Link string
}

// deprecationRecorder is an http.RoundTripper that inspects every response for
// the RFC 9745 `Deprecation` header. It is installed once in New, beneath the
// http.Client, so all request paths in this package — and any added later —
// pass through this single choke point with no per-call-site code.
//
// Dedup is per unique method + concrete request path per client instance:
// Terraform fans a single plan/apply into many REST calls, and one warning per
// unique method+path per run is the useful signal; repeating it per call would
// drown the log. Note the key is the concrete path, so a deprecated
// parameterized route (e.g. /v1/agents/{id}) warns once per distinct resource
// touched, not once total.
type deprecationRecorder struct {
	// next is the real transport (default or the dev-only TLS-skipping one).
	next http.RoundTripper

	// onDeprecation, when non-nil, is invoked exactly once per unique
	// method+path, outside the lock so a slow callback never serializes
	// parallel requests. It receives the originating request's context so
	// context-aware loggers (tflog) work from inside the hook.
	onDeprecation func(ctx context.Context, notice Notice)

	mu      sync.Mutex
	seen    map[string]struct{}
	notices []Notice
}

// RoundTrip forwards the request and, when the response carries a Deprecation
// header for a not-yet-seen method+path, records a Notice and fires the hook.
// The request and response are never mutated, honoring the RoundTripper
// contract.
func (d *deprecationRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := d.next.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	dep := resp.Header.Get("Deprecation")
	if dep == "" {
		return resp, nil
	}

	notice := Notice{
		Method:      req.Method,
		Path:        req.URL.Path,
		Deprecation: dep,
		Sunset:      resp.Header.Get("Sunset"),
		Link:        linkByRel(resp.Header.Values("Link"), "deprecation"),
	}

	key := notice.Method + " " + notice.Path
	d.mu.Lock()
	if _, dup := d.seen[key]; dup {
		d.mu.Unlock()
		return resp, nil
	}
	d.seen[key] = struct{}{}
	d.notices = append(d.notices, notice)
	d.mu.Unlock()

	if d.onDeprecation != nil {
		d.onDeprecation(req.Context(), notice)
	}
	return resp, nil
}

// snapshot returns a copy of the notices recorded so far, in first-seen order,
// so callers can iterate without racing in-flight requests.
func (d *deprecationRecorder) snapshot() []Notice {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Notice, len(d.notices))
	copy(out, d.notices)
	return out
}

// linkByRel extracts the first RFC 8288 Link target whose rel matches rel
// (case-insensitively; a rel value may be a space-separated list of relation
// types, any of which counts). It handles multiple Link header lines as well
// as several comma-separated link-values within one line. The stdlib has no
// Link parser and pulling a dependency for one header is not worth it, so
// this is a small hand parser tolerant of the standard forms.
func linkByRel(headerValues []string, rel string) string {
	for _, hv := range headerValues {
		for _, link := range splitOutside(hv, ',') {
			if target, ok := matchRel(link, rel); ok {
				return target
			}
		}
	}
	return ""
}

// matchRel parses a single `<target>; param=value; …` link-value and reports
// whether its rel parameter contains rel, returning the target when it does.
func matchRel(link, rel string) (string, bool) {
	link = strings.TrimSpace(link)
	if !strings.HasPrefix(link, "<") {
		return "", false
	}
	end := strings.Index(link, ">")
	if end < 0 {
		return "", false
	}
	target := link[1:end]
	for _, param := range splitOutside(link[end+1:], ';') {
		k, v, ok := strings.Cut(strings.TrimSpace(param), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "rel") {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		for _, r := range strings.Fields(v) {
			if strings.EqualFold(r, rel) {
				return target, true
			}
		}
	}
	return "", false
}

// splitOutside splits v on sep occurrences that fall outside a <…> target and
// outside quoted-strings — both may legally contain the separator (a comma in
// a URI, a quoted `title="a, b"`), so a plain strings.Split would corrupt them.
func splitOutside(v string, sep byte) []string {
	var parts []string
	start := 0
	inAngle, inQuote := false, false
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case inQuote:
			if c == '\\' && i+1 < len(v) {
				i++ // quoted-pair: the escaped char cannot close the string
			} else if c == '"' {
				inQuote = false
			}
		case inAngle:
			if c == '>' {
				inAngle = false
			}
		case c == '"':
			inQuote = true
		case c == '<':
			inAngle = true
		case c == sep:
			parts = append(parts, v[start:i])
			start = i + 1
		}
	}
	return append(parts, v[start:])
}

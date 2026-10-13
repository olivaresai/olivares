// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

import (
	"strings"
)

// spiffeID is a SPIFFE ID parsed from either the structured object or the flat
// string export form. It keeps the trust domain and path separately so the
// connector can both reassemble the full id and apply a trust-domain filter.
type spiffeID struct {
	td   string // trust-domain name, no scheme, no path (e.g. "corp.example")
	pth  string // path, leading slash preserved (e.g. "/ns/prod/sa/web"); "" for a bare td
	full string // the reassembled "spiffe://<td><path>"; "" when neither td nor path is present
}

// parseSpiffe parses a flat "spiffe://<td><path>" string into a spiffeID. A value
// missing the scheme is treated as "<td><path>" so a slightly non-conforming
// export still resolves to a trust domain and path.
func parseSpiffe(v string) spiffeID {
	v = strings.TrimSpace(v)
	if v == "" {
		return spiffeID{}
	}
	rest := strings.TrimPrefix(v, scheme)
	td := rest
	path := ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		td = rest[:i]
		path = rest[i:]
	}
	return fromParts(td, path)
}

// fromParts builds a spiffeID from a trust domain and a path, normalizing the path
// to a leading slash and reassembling the canonical full id.
func fromParts(td, path string) spiffeID {
	td = strings.TrimSpace(td)
	td = strings.TrimPrefix(td, scheme)
	path = strings.TrimSpace(path)
	// Defensively strip a trust domain accidentally embedded in the path.
	path = strings.TrimPrefix(path, scheme+td)
	if path != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	out := spiffeID{td: td, pth: path}
	if td == "" && path == "" {
		return out
	}
	out.full = scheme + td + path
	return out
}

// trustDomain returns the bare trust-domain name (no scheme, no path).
func (s spiffeID) trustDomain() string { return s.td }

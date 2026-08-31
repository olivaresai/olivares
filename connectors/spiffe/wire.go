// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

import (
	"encoding/json"
	"strings"
)

// This file holds the JSON wire shapes the connector reads. Only the minimal-data
// fields of a SPIRE registration entry are modeled: a SPIFFE ID, its parent agent
// SPIFFE ID, its selectors and a couple of governance flags. No SVID, key, CA or
// secret field is read — a SPIRE registration entry carries none.

// entriesExport is the top-level shape of a `spire-server entry show -output json`
// export: an "entries" array.
type entriesExport struct {
	Entries []entry `json:"entries"`
}

// entry is one SPIRE registration entry. spiffe_id and parent_id are spiffeID,
// which decodes BOTH the structured object form
//
//	{"trust_domain":"corp.example","path":"/ns/prod/sa/web"}
//
// and the flat string form
//
//	"spiffe://corp.example/ns/prod/sa/web"
type entry struct {
	ID          string     `json:"id"`
	SpiffeID    spiffeID   `json:"spiffe_id"`
	ParentID    spiffeID   `json:"parent_id"`
	Selectors   []selector `json:"selectors"`
	X509SVIDTTL int        `json:"x509_svid_ttl"`
	Admin       bool       `json:"admin"`
}

// selector is a SPIRE selector: a type and value (e.g. {"type":"k8s","value":"ns:prod"}).
type selector struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// spiffeID is a SPIFFE ID parsed from either the structured object or the flat
// string export form. It keeps the trust domain and path separately so the
// connector can both reassemble the full id and apply a trust-domain filter.
type spiffeID struct {
	td   string // trust-domain name, no scheme, no path (e.g. "corp.example")
	pth  string // path, leading slash preserved (e.g. "/ns/prod/sa/web"); "" for a bare td
	full string // the reassembled "spiffe://<td><path>"; "" when neither td nor path is present
}

// structuredSpiffeID is the object form of a SPIFFE ID in a SPIRE export.
type structuredSpiffeID struct {
	TrustDomain string `json:"trust_domain"`
	Path        string `json:"path"`
}

// UnmarshalJSON decodes a SPIFFE ID from either the structured object form or the
// flat string form, normalizing both to the same internal representation. A null
// or empty value yields the zero spiffeID (an entry without a usable id, which the
// mapper skips rather than guessing).
func (s *spiffeID) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*s = spiffeID{}
		return nil
	}
	if trimmed[0] == '"' {
		var flat string
		if err := json.Unmarshal(data, &flat); err != nil {
			return err
		}
		*s = parseSpiffe(flat)
		return nil
	}
	var obj structuredSpiffeID
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	*s = fromParts(obj.TrustDomain, obj.Path)
	return nil
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

// String returns the canonical full SPIFFE ID ("spiffe://<td><path>"), or "" when
// the id is empty/unusable.
func (s spiffeID) String() string { return s.full }

// trustDomain returns the bare trust-domain name (no scheme, no path).
func (s spiffeID) trustDomain() string { return s.td }

// path returns the SPIFFE ID path, used as the Identity/Collection display label.
// For a bare trust domain (no path) it falls back to the trust-domain name so the
// label is never empty.
func (s spiffeID) path() string {
	if s.pth != "" {
		return s.pth
	}
	return s.td
}

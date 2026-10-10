// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package mcpgateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
)

// doMCPForwardRequest guards the actual credential sent by either credential
// provider, before any JSON response or subscription frame can be consumed.
// It sends once and never rewrites upstream content. The same reader handles
// literal matches across reads and decoded JSON strings (including object keys,
// error.data and Unicode escapes) in both JSON and SSE. It does not infer
// arbitrary encodings or combine separate JSON strings into a credential.
func doMCPForwardRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	return doMCPCredentialRequest(client.Do, req)
}

// Inspection uses the same guard below the connector's HTTP client so every
// initialize, notification and catalog page is checked before its consumer.
// Delegate once to the existing transport, retaining its egress policy.
type CredentialTransport struct{ Inner http.RoundTripper }

func (t CredentialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return doMCPCredentialRequest(t.Inner.RoundTrip, req)
}

func doMCPCredentialRequest(send func(*http.Request) (*http.Response, error), req *http.Request) (*http.Response, error) {
	if req.Header.Get("Authorization") == "" && req.URL.User != nil {
		// net/http otherwise adds this Basic header inside Do, after the guard
		// observes the request. Materialize the same header and protect its
		// encoded value plus the password/pair supplied in the operator URL.
		password, _ := req.URL.User.Password()
		req.SetBasicAuth(req.URL.User.Username(), password)
	}
	patterns, err := CredentialPatterns(req.Header.Get("Authorization"))
	if err != nil {
		return nil, err
	}
	resp, err := send(req)
	if err != nil {
		for _, pattern := range patterns {
			if strings.Contains(err.Error(), pattern) {
				return nil, mcpc.ErrUpstreamCredentialDisclosure
			}
		}
		return resp, err
	}
	if len(patterns) != 0 {
		resp.Body = NewCredentialResponseReader(resp.Body, patterns)
	}
	return resp, nil
}

// NewCredentialResponseReader wraps body so reading it fails closed with
// mcpc.ErrUpstreamCredentialDisclosure once any pattern (CredentialPatterns)
// appears, literally or inside a decoded JSON string.
func NewCredentialResponseReader(body io.ReadCloser, patterns []string) io.ReadCloser {
	guard := &mcpCredentialResponseReader{body: body, patterns: patterns}
	for _, pattern := range patterns {
		guard.matchers = append(guard.matchers, newMCPCredentialMatcher(pattern))
	}
	return guard
}

// CredentialPatterns lists the forms of an Authorization header value the guard
// refuses to see echoed back: the header, its credential component and, for Basic,
// the decoded pair and password. A credential too short to match safely is refused.
func CredentialPatterns(header string) (patterns []string, err error) {
	if header == "" {
		return nil, nil
	}
	values := []string{header, strings.TrimSpace(header)}
	component := strings.TrimSpace(header)
	if i := strings.IndexAny(strings.TrimSpace(header), " \t"); i >= 0 {
		component = strings.TrimSpace(strings.TrimSpace(header)[i+1:])
		values = append(values, component)
	}
	// Basic's canonical decoded pair and password are usable credentials too,
	// independent of whether the header came from a static/provider/URL source.
	basic := &http.Request{Header: http.Header{"Authorization": {strings.TrimSpace(header)}}}
	if user, password, ok := basic.BasicAuth(); ok {
		component = password
		values = append(values, user+":"+password, password)
	} else if fields := strings.Fields(header); len(fields) > 0 && strings.EqualFold(fields[0], "Basic") {
		return nil, fmt.Errorf("%w: %w", mcpc.ErrUpstreamCredentialDisclosure, mcpc.ErrUpstreamCredentialInvalid)
	}
	// A short secret cannot be distinguished from incidental prose by substring
	// matching. Refuse it before transmission instead of weakening the guard.
	if len(component) < 8 {
		return nil, fmt.Errorf("%w: %w", mcpc.ErrUpstreamCredentialDisclosure, mcpc.ErrUpstreamCredentialTooShort)
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		if !slices.Contains(patterns, value) {
			patterns = append(patterns, value)
		}
	}
	return patterns, nil
}

// A KMP matcher bounds work to the number of bytes, including long-lived SSE
// streams. It retains only the credential and prefix table, never the stream.
type mcpCredentialMatcher struct {
	pattern string
	prefix  []int
	matched int
}

func newMCPCredentialMatcher(pattern string) mcpCredentialMatcher {
	m := mcpCredentialMatcher{pattern: pattern, prefix: make([]int, len(pattern))}
	for i, j := 1, 0; i < len(pattern); i++ {
		for j > 0 && pattern[i] != pattern[j] {
			j = m.prefix[j-1]
		}
		if pattern[i] == pattern[j] {
			j++
		}
		m.prefix[i] = j
	}
	return m
}

func (m *mcpCredentialMatcher) push(b byte) bool {
	for m.matched > 0 && b != m.pattern[m.matched] {
		m.matched = m.prefix[m.matched-1]
	}
	if b == m.pattern[m.matched] {
		m.matched++
	}
	if m.matched == len(m.pattern) {
		m.matched = m.prefix[m.matched-1]
		return true
	}
	return false
}

type mcpCredentialResponseReader struct {
	body       io.ReadCloser
	patterns   []string
	matchers   []mcpCredentialMatcher
	quoted     bool
	escaped    bool
	stringJSON []byte
	refused    error
}

func (r *mcpCredentialResponseReader) Read(p []byte) (int, error) {
	if r.refused != nil {
		return 0, r.refused
	}
	n, err := r.body.Read(p)
	for _, b := range p[:n] {
		for i := range r.matchers {
			if r.matchers[i].push(b) {
				r.refused = mcpc.ErrUpstreamCredentialDisclosure
				return 0, r.refused
			}
		}
		// A JSON string cannot contain a literal line break. Reset at the SSE
		// line boundary so ignored comments/fields cannot shift JSON quoting.
		if b == '\n' || b == '\r' {
			r.quoted, r.escaped = false, false
			r.stringJSON = r.stringJSON[:0]
			continue
		}
		if !r.quoted {
			if b == '"' {
				r.quoted = true
				r.stringJSON = append(r.stringJSON[:0], b)
			}
			continue
		}
		if len(r.stringJSON) >= mcpForwardMaxResponse {
			r.refused = fmt.Errorf("mcp gateway: upstream JSON string exceeds response limit")
			return 0, r.refused
		}
		r.stringJSON = append(r.stringJSON, b)
		if r.escaped {
			r.escaped = false
			continue
		}
		if b == '\\' {
			r.escaped = true
		} else if b == '"' {
			r.quoted = false
			var decoded string
			if json.Unmarshal(r.stringJSON, &decoded) == nil {
				for _, pattern := range r.patterns {
					if strings.Contains(decoded, pattern) {
						r.refused = mcpc.ErrUpstreamCredentialDisclosure
						return 0, r.refused
					}
				}
			}
			r.stringJSON = r.stringJSON[:0]
		}
	}
	if err != nil {
		for _, pattern := range r.patterns {
			if strings.Contains(err.Error(), pattern) {
				r.refused = mcpc.ErrUpstreamCredentialDisclosure
				return 0, r.refused
			}
		}
	}
	// Returning zero on refusal is essential: a buffered SSE parser must never
	// receive the chunk that completes a credential-bearing notification.
	return n, err
}

func (r *mcpCredentialResponseReader) Close() error { return r.body.Close() }

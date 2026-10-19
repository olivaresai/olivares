// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package egress

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"
)

// Refusal names a request refused for carrying a key other than the session's:
// the destination host and the header name, never a header value.
type Refusal struct{ Host, Header string }

// credentialHeaders are the headers a bound tool sends its provider key in:
// Authorization (bearer; Codex), X-Api-Key (Claude Code) and Api-Key.
var credentialHeaders = []string{"Authorization", "X-Api-Key", "Api-Key"}

// credentialGuard holds the digest of the one key the session is bound to. The
// provider endpoint accepts any key, so an injected agent could otherwise upload
// the workspace to the allowed host under its own.
type credentialGuard struct {
	digest  [sha256.Size]byte
	refused func(Refusal) error
	mu      sync.Mutex
	told    map[Refusal]bool
}

type proxyOption func(*proxy)

// withCredential makes the proxy forward only the policy's own key to a provider.
// A policy without a digest checks nothing.
func withCredential(policy Policy) proxyOption {
	return func(p *proxy) {
		if policy.CredentialSHA256 != ([sha256.Size]byte{}) {
			p.guard = &credentialGuard{digest: policy.CredentialSHA256, refused: policy.OnRefusal, told: map[Refusal]bool{}}
		}
	}
}

// foreign returns the first credential header whose value is not the session's
// key, or "" when every one present is it (or none is). An absent or empty header
// is no credential. Digests are compared in constant time.
func (g *credentialGuard) foreign(h http.Header) string {
	for _, name := range credentialHeaders {
		for _, value := range h.Values(name) {
			value = strings.TrimSpace(value)
			if name == "Authorization" {
				if scheme, rest, ok := strings.Cut(value, " "); ok && strings.EqualFold(scheme, "bearer") {
					value = strings.TrimSpace(rest)
				}
			}
			if value == "" {
				continue
			}
			sum := sha256.Sum256([]byte(value))
			if subtle.ConstantTimeCompare(sum[:], g.digest[:]) != 1 {
				return name
			}
		}
	}
	return ""
}

// refuse tells the policy's owner once per host and header: a loop of refused
// requests is one finding, not one per request. A report that was not kept is
// made again by the next refusal.
func (g *credentialGuard) refuse(host, header string) {
	r := Refusal{Host: host, Header: header}
	g.mu.Lock()
	first := !g.told[r]
	g.told[r] = true
	g.mu.Unlock()
	if !first || g.refused == nil {
		return
	}
	if g.refused(r) != nil {
		g.mu.Lock()
		delete(g.told, r)
		g.mu.Unlock()
	}
}

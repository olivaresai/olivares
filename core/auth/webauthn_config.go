// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"errors"
	"strings"

	"github.com/olivaresai/olivares/core/webaddr"
)

// AN EXPLICIT RELYING PARTY IS NEVER TAKEN ON TRUST, AND NEVER FALLS BACK
// SILENTLY.
//
// Before this, a composition root that read the two environment keys built a
// WebAuthnRP from them with NO predicate at all. An operator who pinned an IP, a
// single-label name, or an origin belonging to a different site got a deployment
// where every ceremony failed, with an opaque answer and nothing naming the
// cause. The value they had written by hand was the one thing nobody checked.
//
// The rule this implements: if an operator declared an explicit relying party, it
// is used or the engine refuses to start. It is never quietly replaced by a
// derived one — a silent fallback turns a typo into an authentication authority
// the operator did not choose.
//
// THE DIAGNOSTIC CARRIES NO VALUE. Not the relying-party ID, not the origins.
// These messages reach stderr, which in every documented deployment of this
// product is a log pipeline; an origin can carry an internal host name, and the
// operator reading the message is looking at the file they just edited. Field and
// class is what they need and all they get.

// ValidateWebAuthnRP checks an explicitly configured relying party and returns it
// in the canonical form a browser compares against.
//
// TWO PREDICATES, because one is not enough and the asymmetry is measured. The
// verifier in this build (go-webauthn v0.17.4, protocol.ValidateRPID) ACCEPTS any
// value net.ParseIP parses, and browsers refuse an IP relying party outright; it
// also refuses any single-label name except exactly "localhost", which is a limit
// of this library rather than of the specification. So the ID is asked both what
// the URL Standard thinks of it and what this build's verifier thinks of it.
//
// Origins are canonicalized the same way a browser reports them — the default
// port dropped, the host lowercased and IDN-encoded — because the library
// compares the collected client-data origin against these strings and drops only
// a literal ":80"/":443".
//
// AN ORIGIN IS NOT REQUIRED TO BE UNDER THE RELYING-PARTY ID, and the check that
// required it is REMOVED rather than softened.
//
// It was added here on the reasoning that a browser refuses any ceremony whose
// origin is not under the relying party, so a pair failing that test "could never
// complete one". That reasoning is wrong, and an independent review demonstrated
// it: WebAuthn Level 3 §5.1.3 continues the ceremony when the RP ID is not a
// registrable suffix of the effective domain, provided the client supports
// RELATED ORIGIN REQUESTS and the §5.11 validation of
// https://<rpId>/.well-known/webauthn passes. The installed verifier agrees — it
// builds that configuration and its origin check asks only whether the origin is
// among the configured ones. The pair booted before this lot existed, and the
// check turned a supported deployment into a refused boot on the strength of a
// sentence nobody had measured.
//
// What is still checked is what this layer can actually decide: the pair is
// complete, the ID is a domain this build's verifier will take, and every origin
// is a real browser origin. Whether a related-origin arrangement WORKS is decided
// in the browser, against a document this engine does not fetch and must not
// publish on the operator's behalf — so accepting the configuration here is
// acceptance of the CONFIGURATION, never a promise of a successful ceremony.
func ValidateWebAuthnRP(rp WebAuthnRP) (WebAuthnRP, error) {
	id := strings.TrimSpace(rp.ID)
	if id == "" {
		return WebAuthnRP{}, errors.New("the relying-party ID is empty")
	}
	canonicalID, kind, code := webaddr.ParseHost(id)
	if code != 0 {
		return WebAuthnRP{}, errors.New("the relying-party ID is not a host name (it must be a bare domain: no scheme, no port, no path)")
	}
	if kind != webaddr.KindDomain {
		return WebAuthnRP{}, errors.New("the relying-party ID is an IP address. A browser refuses a passkey ceremony whose relying party is an address, even though the verifier in this build accepts the string; reach the console by a name instead")
	}
	if !webaddr.IsRelyingPartyDomain(canonicalID) {
		return WebAuthnRP{}, errors.New("the relying-party ID is not a domain the verifier in this build accepts: it must be a valid domain — which rules out an underscore, a leading or trailing hyphen and an empty label — and, unless it is exactly \"localhost\", it must contain a dot")
	}
	if len(rp.Origins) == 0 {
		return WebAuthnRP{}, errors.New("no relying-party origins were given; a ceremony is verified against the exact origins the console is served on")
	}
	origins := make([]string, 0, len(rp.Origins))
	seen := make(map[string]struct{}, len(rp.Origins))
	for _, raw := range rp.Origins {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		addr, err := webaddr.Parse("a relying-party origin", raw)
		if err != nil {
			return WebAuthnRP{}, err
		}
		if _, dup := seen[addr.Origin]; dup {
			continue
		}
		seen[addr.Origin] = struct{}{}
		origins = append(origins, addr.Origin)
	}
	if len(origins) == 0 {
		return WebAuthnRP{}, errors.New("no relying-party origins were given; a ceremony is verified against the exact origins the console is served on")
	}
	name := strings.TrimSpace(rp.DisplayName)
	if name == "" {
		name = defaultWebAuthnDisplayName
	}
	return WebAuthnRP{ID: canonicalID, DisplayName: name, Origins: origins}, nil
}

// defaultWebAuthnDisplayName is what an authenticator shows when the operator
// named no display name.
const defaultWebAuthnDisplayName = "Olivares AI"

// WebAuthnRPFromAddress derives a relying party from the address an operator
// declared this console is reached at, or reports that the address cannot be one.
//
// A declared address that is NOT a usable relying party — an IP, a single-label
// name, a host a browser opens but the URL Standard does not call a valid domain
// — is still a perfectly good browser address. It is simply not authority for a
// passkey, and the caller must preserve that as an explicit unavailable outcome
// rather than quietly deriving a relying party from whatever Host header the next
// request happens to carry.
func WebAuthnRPFromAddress(a webaddr.Address) (WebAuthnRP, bool) {
	if a.IsZero() || !a.CanBeRelyingParty() {
		return WebAuthnRP{}, false
	}
	return WebAuthnRP{ID: a.Host, DisplayName: defaultWebAuthnDisplayName, Origins: []string{a.Origin}}, true
}

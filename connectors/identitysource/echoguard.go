// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package identitysource

import (
	"errors"
	"fmt"
	"strings"
)

// Credential-echo guard (H-01/H-02 of the 26.10.x security backlog).
//
// The lifecycle contract has always REQUIRED that "all errors must be
// non-sensitive: no credential, no token, no secret material ever rides an
// error string", but a requirement in a comment does not enforce itself: the
// actuator talks to a CONFIGURED authority (the operator's own secret manager),
// and a broken or hostile one can answer with the very credential that
// authenticated the request — as the minted "new secret", or inside an error
// excerpt. The admin-tier caller of Rotate and the readers of the persisted
// failure trail do not hold that credential, so echoing it hands it to them.
//
// These two helpers are the enforcement: the actuator, the one component that
// KNOWS the credential it sent, applies them at the trust boundary — refusing
// an echoed minted secret (RejectEchoedSecret) and scrubbing the sent
// credential out of any excerpt that may be persisted or logged
// (ScrubCredentials). A well-behaved authority never echoes, so for one the
// behavior is byte-identical to before.

// ErrCredentialEcho marks the refusal of an upstream answer that reproduces the
// credential sent in the request: the configured authority is broken or
// hostile. Consumers can errors.Is it to discriminate this class from an
// ordinary actuation failure (it is NOT retryable: the authority must be
// investigated first).
var ErrCredentialEcho = errors.New("identitysource: upstream answer reproduces the request credential")

// minSentCredentialLen is the floor for the SUBSTRING ("contains") test only:
// below it a needle would false-positive inside legitimate secrets and
// excerpts. Exact equality is always checked (see RejectEchoedSecret), and
// ScrubCredentials removes every nonempty credential outright.
const minSentCredentialLen = 8

// removedCredentialMarker replaces scrubbed credential bytes: the excerpt
// keeps saying WHAT was removed, never the value.
const removedCredentialMarker = "[removed: request credential]"

// ScrubCredentials removes every occurrence of the sent credentials from an
// excerpt that may be persisted or logged (H-02): a broken or hostile authority
// can reflect the request credential inside an error body, and an excerpt that
// reaches the NHI failure trail, an audit record or a log must not carry it.
// It reports whether anything was removed. Scrubbing runs BEFORE any length
// truncation at the call site, so a credential cannot survive by straddling the
// cut. A well-behaved authority never echoes, so for one the excerpt is
// byte-identical (removed == false).
//
// EVERY nonempty sent credential is scrubbed, however short (independent security review; Root's
// adjudication: scrub, do not drop the excerpt — the non-secret status text
// stays useful). A short configured credential (Vault's documented dev token
// "root") is still a credential; removing its exact bytes from a diagnostic
// can only mangle a word, never expose anything.
func ScrubCredentials(excerpt string, sentCredentials ...string) (scrubbed string, removed bool) {
	scrubbed = excerpt
	for _, cred := range sentCredentials {
		if cred == "" {
			continue
		}
		if strings.Contains(scrubbed, cred) {
			scrubbed = strings.ReplaceAll(scrubbed, cred, removedCredentialMarker)
			removed = true
		}
	}
	return scrubbed, removed
}

// RejectEchoedSecret refuses a freshly returned secret that equals or CONTAINS
// a credential that authenticated the request (H-01): a well-behaved authority
// mints independent material, so an echo means the authority is broken or
// hostile and the "new secret" is the operator's own credential about to be
// handed to the rotation caller. The error wraps ErrCredentialEcho and says
// WHAT was refused — never the value.
//
// Exact equality is checked for EVERY nonempty credential (independent security review: a
// configured short token is a supported credential — HashiCorp's own setup
// tutorial uses the short dev token ID "root" — and equality with it is an
// unambiguous echo; the floor must not silently exempt it). The
// minSentCredentialLen floor applies ONLY to the substring test: a tiny needle
// would false-positive inside legitimate minted material, so a short
// credential is matched only exactly.
func RejectEchoedSecret(secret string, sentCredentials ...string) error {
	if secret == "" {
		return nil
	}
	for _, cred := range sentCredentials {
		if cred == "" {
			continue
		}
		if secret == cred || (len(cred) >= minSentCredentialLen && strings.Contains(secret, cred)) {
			return fmt.Errorf("%w: refusing the returned secret (the configured authority is broken or hostile; investigate it before retrying)", ErrCredentialEcho)
		}
	}
	return nil
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/webaddr"
)

// THE ASYMMETRY THAT MAKES ONE PREDICATE INSUFFICIENT, pinned against the
// LIBRARY itself rather than against a belief about it.
//
// go-webauthn's protocol.ValidateRPID accepts any value net.ParseIP parses. A
// browser refuses an IP relying party outright. So a validator that asked only
// the library would accept a pin that can never complete a ceremony anywhere —
// which is exactly the deployment this work exists to stop shipping.
//
// The control positive is the first half of this test: if the library ever
// started refusing an IP, the second half would still pass and the extra
// predicate would look like dead weight. It asserts the library's behavior so
// the reason stays visible.
func TestTheVerifierAcceptsAnIPAndThisConfigurationDoesNot(t *testing.T) {
	t.Parallel()
	for _, ip := range []string{"10.1.2.3", "127.0.0.1", "::1"} {
		if err := protocol.ValidateRPID(ip); err != nil {
			t.Fatalf("the installed verifier now REFUSES %q (%v); the second predicate's stated reason is stale and must be re-derived", ip, err)
		}
		if _, err := auth.ValidateWebAuthnRP(auth.WebAuthnRP{
			ID: ip, Origins: []string{"https://" + ip},
		}); err == nil {
			t.Errorf("ValidateWebAuthnRP accepted the IP %q the verifier would take and no browser will", ip)
		}
	}
	// And the other half of the asymmetry: a name the URL Standard calls a valid
	// domain but the installed verifier refuses.
	if err := protocol.ValidateRPID("olivares"); err == nil {
		t.Fatal("the installed verifier now ACCEPTS a single-label name; the copy that attributes this limit to the library must be re-derived")
	}
}

func TestValidateWebAuthnRPCanonicalizesWhatABrowserWillSend(t *testing.T) {
	t.Parallel()
	got, err := auth.ValidateWebAuthnRP(auth.WebAuthnRP{
		ID: "Panel.Example.COM",
		// The default port is not part of an origin a browser reports, and the
		// library drops only the LITERAL ":443" — so ":0443" would fail every
		// finish leg. Canonicalizing here is what stops that.
		Origins: []string{"HTTPS://Panel.Example.COM:0443", "https://panel.example.com", "https://ops.panel.example.com:8443"},
	})
	if err != nil {
		t.Fatalf("ValidateWebAuthnRP: %v", err)
	}
	if got.ID != "panel.example.com" {
		t.Errorf("id = %q, want panel.example.com", got.ID)
	}
	// The first two spellings are the SAME origin and collapse to one.
	want := []string{"https://panel.example.com", "https://ops.panel.example.com:8443"}
	if strings.Join(got.Origins, "|") != strings.Join(want, "|") {
		t.Errorf("origins = %v, want %v", got.Origins, want)
	}
	if got.DisplayName == "" {
		t.Error("an authenticator would be shown an empty relying-party name")
	}
}

// A CONFIGURATION REFUSAL CARRIES NO CONFIGURED VALUE. Origins are named
// explicitly in the rule because an origin is the component most likely to carry
// an internal host name, and these errors are printed on stderr, which in every
// documented deployment of this product is a log pipeline.
func TestAConfigurationRefusalDoesNotEchoTheConfiguration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		rp     auth.WebAuthnRP
		secret string
	}{
		{auth.WebAuthnRP{ID: "internal-vault.corp", Origins: []string{"https://internal-vault.corp/secret-path"}}, "secret-path"},
		{auth.WebAuthnRP{ID: "internal-vault.corp", Origins: []string{"https://internal-vault.corp?tk=t0kenvalue"}}, "t0kenvalue"},
		{auth.WebAuthnRP{ID: "internal-vault.corp", Origins: []string{"https://ops:S3cretPass@internal-vault.corp"}}, "S3cretPass"},
		// Not a relation failure — related origins are accepted (see the test
		// above). This row refuses because the ID is an IP.
		{auth.WebAuthnRP{ID: "10.9.8.7", Origins: []string{"https://10.9.8.7"}}, "10.9.8.7"},
	}
	for _, c := range cases {
		_, err := auth.ValidateWebAuthnRP(c.rp)
		if err == nil {
			t.Errorf("%+v was accepted; this row exists to check its refusal", c.rp)
			continue
		}
		if strings.Contains(err.Error(), c.secret) {
			t.Errorf("the refusal echoes %q: %s", c.secret, err)
		}
		// Not satisfiable by saying nothing: it has to name the component.
		if !strings.Contains(err.Error(), "relying-party") {
			t.Errorf("the refusal names no component: %s", err)
		}
	}
}

// A registrable PARENT domain and several origins under it stay supported: the
// ID is required to be the origin's host or above it, never equal to it.
// A RELATED ORIGIN IS ACCEPTED CONFIGURATION, and this test is the record of a
// refusal that was removed. WebAuthn Level 3 §5.11 lets a relying party name
// origins that are NOT under its ID, validated in the browser against
// https://<rpId>/.well-known/webauthn. The installed verifier builds the
// configuration and its origin check asks only whether the origin is configured.
// A check added here refused the pair at boot on the false ground that a browser
// could never complete it; an independent review measured otherwise.
//
// Accepting it is acceptance of the CONFIGURATION. Whether the ceremony succeeds
// depends on client support and on a document this engine neither fetches nor
// publishes, and nothing in this package claims otherwise.
func TestARelatedOriginIsAcceptedConfigurationAndNotAPromise(t *testing.T) {
	t.Parallel()
	got, err := auth.ValidateWebAuthnRP(auth.WebAuthnRP{
		ID:      "example.com",
		Origins: []string{"https://console.example.net", "https://panel.example.com"},
	})
	if err != nil {
		t.Fatalf("a related-origin pair was refused at boot: %v", err)
	}
	if got.ID != "example.com" {
		t.Errorf("rp id = %q, want example.com", got.ID)
	}
	want := []string{"https://console.example.net", "https://panel.example.com"}
	if strings.Join(got.Origins, "|") != strings.Join(want, "|") {
		t.Errorf("origins = %v, want both kept in order %v", got.Origins, want)
	}
	// The non-firing direction: an origin that is not a browser ORIGIN is still
	// refused, so removing the relation check did not remove origin validation.
	for _, bad := range []string{"https://console.example.net/app", "not-a-url", "https://console.example.net?a=1"} {
		if _, err := auth.ValidateWebAuthnRP(auth.WebAuthnRP{
			ID: "example.com", Origins: []string{bad},
		}); err == nil {
			t.Errorf("origin %q was accepted; per-origin validation must survive", bad)
		}
	}
}

func TestAParentDomainAndSeveralOriginsStaySupported(t *testing.T) {
	t.Parallel()
	got, err := auth.ValidateWebAuthnRP(auth.WebAuthnRP{
		ID:      "example.com",
		Origins: []string{"https://panel.example.com", "https://ops.eu.example.com:8443", "https://example.com"},
	})
	if err != nil {
		t.Fatalf("a registrable parent domain was refused: %v", err)
	}
	if len(got.Origins) != 3 {
		t.Fatalf("origins = %v, want all three kept", got.Origins)
	}
	// The trailing root dot is not a label, on either side of the comparison.
	if _, err := auth.ValidateWebAuthnRP(auth.WebAuthnRP{
		ID: "example.com.", Origins: []string{"https://panel.example.com"},
	}); err != nil {
		t.Errorf("a trailing root dot on the ID was treated as a label: %v", err)
	}
}

// WebAuthnRPFromAddress reports the THIRD state rather than inventing a relying
// party: "this is a fine browser address and it is not authority for a passkey".
func TestWebAuthnRPFromAddress(t *testing.T) {
	t.Parallel()
	for raw, wantID := range map[string]string{
		"https://panel.example.com:8443": "panel.example.com",
		"https://localhost:8443":         "localhost",
		"https://10.0.0.7:8443":          "",
		"https://[2001:db8::1]":          "",
		"https://olivares":               "",
		"https://my_host.example.com":    "",
		"":                               "",
	} {
		addr, err := webaddr.Parse("--public-url", raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", raw, err)
		}
		rp, ok := auth.WebAuthnRPFromAddress(addr)
		if ok != (wantID != "") {
			t.Errorf("%q: usable = %v, want %v", raw, ok, wantID != "")
		}
		if rp.ID != wantID {
			t.Errorf("%q: rp id = %q, want %q", raw, rp.ID, wantID)
		}
		if ok && (len(rp.Origins) != 1 || rp.Origins[0] != addr.Origin) {
			t.Errorf("%q: origins = %v, want [%s]", raw, rp.Origins, addr.Origin)
		}
	}
}

// AN EXPLICIT PIN IS NEVER SILENTLY REWRITTEN, in any spelling.
//
// A compatibility spelling of the punycode prefix used to walk past a guard that
// matched the literal four bytes, and UTS-46 then erased the label: the pin
// `panel.<fullwidth xn-->` was ACCEPTED and rewritten to the relying-party ID
// "panel." — a different authority from the one the operator typed, installed
// without a word. An independent review measured it. Clause 1 says an explicit
// pair is used or refused, never replaced.
func TestAPinIsNeverRewrittenByAMappingThatErasesALabel(t *testing.T) {
	t.Parallel()
	for _, id := range []string{
		"panel.ｘｎ－－",  // fullwidth x n - -
		"panel.xn－－",  // fullwidth hyphens
		"panel.xn-­-", // a soft hyphen inside the prefix
		"panel.­xn--", // and in front of it
		"panel.xn--",  // the literal spelling
	} {
		got, err := auth.ValidateWebAuthnRP(auth.WebAuthnRP{
			ID: id, Origins: []string{"https://panel.example.com"},
		})
		if err == nil {
			t.Errorf("pin %q was accepted and became %q", id, got.ID)
			continue
		}
		if strings.Contains(err.Error(), id) {
			t.Errorf("the refusal echoes the configured value: %v", err)
		}
	}
	// The non-firing direction: a REAL punycode label is still a relying party, so
	// this did not become a ban on internationalized names.
	got, err := auth.ValidateWebAuthnRP(auth.WebAuthnRP{
		ID: "xn--bcher-kva.example", Origins: []string{"https://xn--bcher-kva.example"},
	})
	if err != nil {
		t.Fatalf("a valid ACE label was refused: %v", err)
	}
	if got.ID != "xn--bcher-kva.example" {
		t.Fatalf("rp id = %q, want it unchanged", got.ID)
	}
}

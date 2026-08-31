// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// The cross-language half of the ISSUING ROUTE — not of the encoder.
//
// WHY A SECOND VECTOR EXISTS ALONGSIDE credential_v3_ts_vector.json. That one is built from a
// hand-written IssueContext: it proves the TypeScript encoder and this parser agree on BYTES,
// and it is silent about the VALUES the fulfillment route chooses. Those values are decided in
// commercial/license-worker/src/license/issue-context.ts — the entity id taken from the
// provider's customer, a deployment id derived because "the purchase creates deployment #1",
// purpose "production", the clock fields deliberately omitted for want of an activated
// deployment, and the refund-window phase with its 14-day guarantee and 72 h lease.
//
// EVERY ONE OF THOSE IS A FIELD THIS PARSER CAN REFUSE. A blank entity_id or deployment_id is a
// hard error here, an unrecognized purpose is a hard error, a provisional grant without a
// promotion_hold_deadline to clamp against is a hard error, and a non-zero max_users is a hard
// error. Without this test the first place any of that would surface is the buyer's engine,
// after we had already taken their money and mailed them the blob.
//
// Regenerate the vector deliberately, never automatically:
//   cd commercial/license-worker && OLIVARES_WRITE_V3_ROUTE_VECTOR=1 node --test test/dodo-issue.test.ts
// That makes the TypeScript side green and leaves THIS test red until it is re-run, which is the
// correct order: a vector must never be able to bless itself.

package license

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const v3RouteVectorPath = "testdata/credential_v3_route_vector.json"

func TestV3RouteVectorIsAcceptedByTheVerifier(t *testing.T) {
	payload, err := os.ReadFile(filepath.Clean(v3RouteVectorPath))
	if err != nil {
		t.Fatalf("the route vector is missing (%v).\n"+
			"Regenerate it from the issuing route:\n"+
			"  cd commercial/license-worker && OLIVARES_WRITE_V3_ROUTE_VECTOR=1 node --test test/dodo-issue.test.ts", err)
	}

	c, err := ParseCredentialV3(payload)
	if err != nil {
		t.Fatalf("the verifier REFUSES what the fulfillment route issues: %v\n"+
			"A paying customer would receive a credential their engine cannot read.", err)
	}

	// The envelope fields the route DERIVES rather than reads off the body. Asserted by shape,
	// not by exact value, because the ids belong to the captured corpus and this test is about
	// the route's rules holding — a value assertion would go red on a corpus refresh that
	// changed nothing about the contract.
	if c.EntityID == "" || c.Deployment == "" {
		t.Fatalf("entity_id %q / deployment_id %q: the wire requires both non-empty", c.EntityID, c.Deployment)
	}
	if !strings.HasPrefix(c.Deployment, "dep_") {
		t.Errorf("deployment_id %q does not name a deployment; the purchase is supposed to create #1", c.Deployment)
	}
	if c.Purpose != "production" {
		t.Errorf("purpose = %q, want production: the self-serve cycle sells production, and staging is `olivares enterprise register`'s", c.Purpose)
	}
	if !strings.HasPrefix(c.KeyID, "sha256:") {
		t.Errorf("key_id = %q, want a digest of the signing key: a configured id can name a key nobody checked it against", c.KeyID)
	}
	if c.MaxUsers != 0 {
		t.Errorf("max_users = %d, want 0 (unlimited since B10)", c.MaxUsers)
	}

	// ONE GRANT PER PURCHASED LINE, which is the whole reason v3 exists: the flat container
	// would have needed a rule for combining a base and its add-ons, and that rule was nobody's
	// to invent inside a signed artifact.
	if len(c.Grants) < 2 {
		t.Fatalf("grants = %d, want the base and at least one add-on: every positive cohort in the corpus carries one", len(c.Grants))
	}
	bases := 0
	for _, g := range c.Grants {
		if g.Kind == GrantKindBase {
			bases++
		}
		// A first purchase is inside the voluntary money-back window, so it runs on a lease.
		// PRICING-CANON.md:176-179 (`refund_window_issuance.connected.provisional: true`).
		if g.Phase != PhaseRefundWindow {
			t.Errorf("grant %s phase = %q, want refund_window for a first purchase", g.OrderLineID, g.Phase)
		}
		if g.LeaseUntil.IsZero() {
			t.Errorf("grant %s carries no lease_until, so a provisional grant has no runway", g.OrderLineID)
		}
		if g.GuaranteeDeadline.IsZero() {
			t.Errorf("grant %s carries no guarantee_deadline; the 14-day window is what makes it provisional", g.OrderLineID)
		}
		if g.ExpiresAt.After(g.PaidThrough) {
			t.Errorf("grant %s expires %s past paid_through %s", g.OrderLineID, g.ExpiresAt, g.PaidThrough)
		}
	}
	if bases != 1 {
		t.Errorf("base grants = %d, want exactly 1 (every-addon-requires-effective-business-grant)", bases)
	}
}

// The control. Without it, a ParseCredentialV3 that returned success for anything would make the
// test above pass on a vector that says nothing at all.
func TestV3RouteVectorRejectsATamperedCopy(t *testing.T) {
	payload, err := os.ReadFile(filepath.Clean(v3RouteVectorPath))
	if err != nil {
		t.Skipf("route vector missing: %v", err)
	}
	// Blank the entity id — the field the route takes verbatim from the provider's customer, and
	// the one whose emptiness a "helpful" issuer would be most tempted to tolerate.
	broken := strings.Replace(string(payload), `"entity_id":"`, `"entity_id":"","x":"`, 1)
	if broken == string(payload) {
		t.Fatal("the mutation did not apply; this control proves nothing")
	}
	if _, err := ParseCredentialV3([]byte(broken)); err == nil {
		t.Fatal("a credential with a blank entity_id was accepted; the verifier is not discriminating")
	}
}

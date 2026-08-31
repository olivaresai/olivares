// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// The cross-language half of the v3 contract: this reads the vector the TypeScript ISSUER
// writes and proves this verifier accepts it.
//
// WHY IT EXISTS. There are two implementations of one wire format —
// commercial/license-worker/src/license/credential-v3.ts writes it, credential_v3.go reads it —
// and two implementations that were never compared are two wire formats. The Dodo adapter
// already learned this the expensive way: its signature verifier and the Go one agree only
// because dodo-conformance.run.ts asserts it, and before that check existed the two derivations
// produced different MACs from the same secret.
//
// The vector is produced from a REAL purchase in the captured corpus (delivery-0011 +
// delivery-0012: a base product and one add-on, 22800 pre-tax), and it deliberately carries its
// two grants in DIFFERENT PHASES. A single-phase vector would never catch a verifier that
// assumed one phase for a whole credential, which is precisely the assumption the flat container
// forced and that v3 exists to remove.
//
// If this test fails after a change to the TS issuer, the question it answers is "does the
// verifier still accept what we issue" — and the answer is no. Regenerating the vector
// (OLIVARES_WRITE_V3_VECTOR=1 on the TS side) makes the TS test green again and leaves THIS one
// red, which is the correct order: the vector must never be able to bless itself.

package license

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const v3VectorPath = "testdata/credential_v3_ts_vector.json"

func TestV3VectorFromTheTypeScriptIssuerIsAccepted(t *testing.T) {
	payload, err := os.ReadFile(filepath.Clean(v3VectorPath))
	if err != nil {
		t.Fatalf("the cross-language vector is missing (%v).\n"+
			"Regenerate it from the issuer:\n"+
			"  cd commercial/license-worker && OLIVARES_WRITE_V3_VECTOR=1 node --test test/credential-v3.test.ts", err)
	}

	c, err := ParseCredentialV3(payload)
	if err != nil {
		t.Fatalf("the verifier REFUSES what the issuer produces: %v\n"+
			"The two halves of the wire format have drifted apart.", err)
	}

	if c.Schema != CredentialSchemaV3 {
		t.Fatalf("schema = %q", c.Schema)
	}
	if len(c.Grants) != 2 {
		t.Fatalf("grants = %d, want 2 (a base and its add-on, from the real corpus pair)", len(c.Grants))
	}

	// The property the container exists for. Asserted here and not only on the TS side, because
	// a verifier that collapsed the phases would still parse a mixed credential and then answer
	// questions about it wrongly.
	base, addon := c.Grants[0], c.Grants[1]
	if base.Kind != GrantKindBase || addon.Kind != GrantKindAddon {
		t.Fatalf("kinds = %q/%q, want base/addon", base.Kind, addon.Kind)
	}
	if base.Phase == addon.Phase {
		t.Fatalf("both grants are in phase %q — the vector must exercise mixed_phase_allowed", base.Phase)
	}
	if base.Phase != PhaseTerm {
		t.Fatalf("base phase = %q, want term", base.Phase)
	}
	if addon.Phase != PhaseRefundWindow {
		t.Fatalf("add-on phase = %q, want refund_window", addon.Phase)
	}

	// The two invariants that are comparisons BETWEEN lines, which is why both fields are
	// per-grant and not envelope fields.
	if addon.PaidThrough.After(base.PaidThrough) {
		t.Fatal("the add-on is paid past its base")
	}
	if base.LeaseUntil.IsZero() != true {
		t.Fatal("a promoted grant must carry no lease")
	}
	if addon.LeaseUntil.IsZero() {
		t.Fatal("a provisional grant must carry a lease")
	}

	// And the boundaries the two phases produce differ, which is the whole point of carrying
	// them separately: at an instant inside the base term but past the add-on lease, exactly one
	// of the two grants is active.
	at := addon.EffectiveBoundary().Add(time.Second)
	active := c.ActiveGrants(at)
	if len(active) != 1 || active[0].Kind != GrantKindBase {
		t.Fatalf("at %s exactly the base must be active, got %d grant(s)", at.Format(time.RFC3339), len(active))
	}
}

func TestV3VectorIsNotTriviallyAccepted(t *testing.T) {
	// The control. Without it, ParseCredentialV3 could be returning success for anything and the
	// test above would still pass — the same "a duplicate check that says yes to everything"
	// failure the Dodo route battery guards against.
	payload, err := os.ReadFile(filepath.Clean(v3VectorPath))
	if err != nil {
		t.Skipf("vector missing: %v", err)
	}
	// One byte of the schema, changed. Nothing else.
	broken := make([]byte, len(payload))
	copy(broken, payload)
	i := 0
	for ; i < len(broken)-3; i++ {
		if string(broken[i:i+3]) == "v3\"" {
			broken[i+1] = '4'
			break
		}
	}
	if i >= len(broken)-3 {
		t.Fatal("could not find the schema version in the vector to mutate it")
	}
	if _, err := ParseCredentialV3(broken); err == nil {
		t.Fatal("a credential naming schema v4 was accepted under v3 rules")
	}
}

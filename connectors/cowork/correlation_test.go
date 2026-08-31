// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import "testing"

// TestOTELComplianceCorrelationKey proves the OTEL↔Compliance correlation contract
// (A1): the shared account identifier resolves to the IDENTICAL
// identity.account reference whether it arrives as Cowork OTEL's user.account_id or as
// the Compliance API's actor.user_id. Because both sources land on the same
// (ResourceKind=identity.account, ref=<account id>) entity, the inventory account
// entity is the join point — a consumer can pivot from a Compliance activity record
// to the Cowork OTEL events for the same account without a content merge.
func TestOTELComplianceCorrelationKey(t *testing.T) {
	const accountID = "user_01SHARED"

	// OTEL side: the account materialized by the cowork connector's identity edge.
	ev := coworkEvent{sessionID: "sess-1", accountID: accountID, accountUUID: "uuid-x", orgID: "org-9"}
	var otelAcctKind, otelAcctRef string
	for _, e := range identityEdges(ev.identity(), testTime) {
		if e.ResourceKind == resIdentityAccount {
			otelAcctKind, otelAcctRef = e.ResourceKind, e.ResourceRef
		}
	}

	// Compliance side: a consumer derives the same join key from the Compliance feed's
	// actor.user_id via the exported AccountRef contract (the Compliance feed carries
	// actor.user_id; AccountRef yields the canonical ref both sides agree on).
	const complianceActorUserID = accountID
	complianceAcctRef := AccountRef(complianceActorUserID, "", "")

	if otelAcctKind != resIdentityAccount {
		t.Fatalf("OTEL side did not materialize an identity.account edge")
	}
	if otelAcctRef != complianceAcctRef {
		t.Errorf("correlation join key mismatch: OTEL=%q Compliance=%q", otelAcctRef, complianceAcctRef)
	}
	if otelAcctRef != accountID {
		t.Errorf("join key = %q, want the shared account id %q", otelAcctRef, accountID)
	}
}

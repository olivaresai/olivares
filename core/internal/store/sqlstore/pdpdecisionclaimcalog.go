// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "github.com/olivaresai/olivares/core/model"

var pdpDecisionClaimDescriptor = model.EntityDescriptor{
	Kind:  "core.pdp_decision_claim",
	Table: "pdp_decision_claims",
	Fields: []model.FieldSpec{
		pdecl(field("target_tenant_id", model.KindUUID, false),
			model.None("the governed tenant, compared with the PEP's tenant on a retry: core/auth/delegation.go:634")),
		pdecl(field("handle_jti", model.KindUUID, false),
			model.None("the delegation handle row, compared on a retry: core/auth/delegation.go:632")),
		pdecl(field("pep_service_id", model.KindUUID, false),
			model.None("the PEP service row, compared on a retry: core/auth/delegation.go:631")),
		pdecl(field("nonce_hash", model.KindText, false),
			model.None("a SHA-256 of the presented nonce: core/auth/delegation.go:591")),
		pdecl(field("request_fingerprint", model.KindText, false),
			model.None("a SHA-256 request fingerprint, compared on a retry: core/auth/delegation.go:1271, core/auth/delegation.go:633")),
		field("request_issued_at", model.KindTimestamp, false),
		pdecl(indexedField("state", model.KindText, false),
			model.None("pending or final only: core/internal/store/sqlstore/claimdecision.go:64, core/internal/store/sqlstore/claimdecision.go:320")),
		pdecl(field("verdict_json", model.KindJSON, true),
			model.None("a verdict document that is only checked to be JSON, hashed and handed back verbatim, never parsed: core/auth/delegation.go:786, core/auth/delegation.go:789, core/auth/delegation.go:651")),
		pdecl(field("verdict_hash", model.KindText, true),
			model.None("a SHA-256 of the verdict, compared for idempotency: core/auth/delegation.go:789, core/auth/delegation.go:874")),
		field("capability_version", model.KindInt, true),
		pdecl(field("effective_capabilities", model.KindJSON, true),
			pdeclBoolVector("PEP capability names from a closed vocabulary: core/auth/delegation.go:975")),
		pdecl(field("policy_version", model.KindText, true),
			model.None("a policy snapshot identifier, only compared for idempotency: core/auth/delegation.go:874")),
		field("claimed_at", model.KindTimestamp, false),
		field("finalized_at", model.KindTimestamp, true),
		// evidence_anchored is true IFF the most recent REQUIRED per-operation audit
		// for this row is durable; a DEGRADE-mode drop leaves it false and the row is a
		// deny-closed tombstone. Non-nullable with a deny-closed false default: this is a
		// NEW table (no legacy rows), so ClaimDecision always sets it explicitly at insert.
		field("evidence_anchored", model.KindBool, false),
	},
	Indexes: []model.IndexSpec{
		{
			Name:    "pdp_decision_claims_handle_jti_uniq",
			Columns: []string{"tenant_id", "handle_jti"},
			Unique:  true,
		},
		{
			Name:    "pdp_decision_claims_service_nonce_uniq",
			Columns: []string{"tenant_id", "pep_service_id", "nonce_hash"},
			Unique:  true,
		},
		// Retention-sweep index for the future claim-GC: the later service stage
		// selects finalizable/reapable claims by (state, claimed_at) within a
		// tenant. Landed now as a descriptor index so the table is created with it;
		// no background loop is wired in this stage.
		{
			Name:    "pdp_decision_claims_state_claimed_at_idx",
			Columns: []string{"tenant_id", "state", "claimed_at"},
		},
	},
}

var pdpDecisionClaimCodec = model.Codec[model.PDPDecisionClaim]{
	Base: func(c *model.PDPDecisionClaim) *model.BaseFields { return &c.BaseFields },
	Encode: func(c model.PDPDecisionClaim) (model.Record, error) {
		effectiveCapabilities, err := encBools(c.EffectiveCapabilities)
		if err != nil {
			return nil, err
		}
		return model.Record{
			"target_tenant_id": encTenant(c.TargetTenantID),
			"handle_jti":       c.HandleJTI.String(), "pep_service_id": c.PEPServiceID.String(),
			"nonce_hash": c.NonceHash, "request_fingerprint": c.RequestFingerprint,
			"request_issued_at": encTS(c.RequestIssuedAt), "state": c.State,
			"verdict_json": encOptStr(c.VerdictJSON), "verdict_hash": encOptStr(c.VerdictHash),
			"capability_version":     encOptInt(int64(c.CapabilityVersion)),
			"effective_capabilities": effectiveCapabilities,
			"policy_version":         encOptStr(c.PolicyVersion), "claimed_at": encTS(c.ClaimedAt),
			"finalized_at": encOptTS(c.FinalizedAt), "evidence_anchored": c.EvidenceAnchored,
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.PDPDecisionClaim, error) {
		requestIssuedAt, err := decTS(r, "request_issued_at")
		if err != nil {
			return model.PDPDecisionClaim{}, err
		}
		effectiveCapabilities, err := decBools(r, "effective_capabilities")
		if err != nil {
			return model.PDPDecisionClaim{}, err
		}
		claimedAt, err := decTS(r, "claimed_at")
		if err != nil {
			return model.PDPDecisionClaim{}, err
		}
		finalizedAt, err := decOptTS(r, "finalized_at")
		if err != nil {
			return model.PDPDecisionClaim{}, err
		}
		return model.PDPDecisionClaim{
			BaseFields: b, TargetTenantID: decTenant(r, "target_tenant_id"),
			HandleJTI:    decID(r, "handle_jti"),
			PEPServiceID: decID(r, "pep_service_id"), NonceHash: r.String("nonce_hash"),
			RequestFingerprint: r.String("request_fingerprint"), RequestIssuedAt: requestIssuedAt,
			State: r.String("state"), VerdictJSON: r.String("verdict_json"),
			VerdictHash: r.String("verdict_hash"), CapabilityVersion: int(r.Int("capability_version")),
			EffectiveCapabilities: effectiveCapabilities, PolicyVersion: r.String("policy_version"),
			ClaimedAt: claimedAt, FinalizedAt: finalizedAt,
			EvidenceAnchored: r.Bool("evidence_anchored"),
		}, nil
	},
}

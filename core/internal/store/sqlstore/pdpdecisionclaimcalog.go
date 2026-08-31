// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "github.com/olivaresai/olivares/core/model"

var pdpDecisionClaimDescriptor = model.EntityDescriptor{
	Kind:  "core.pdp_decision_claim",
	Table: "pdp_decision_claims",
	Fields: []model.FieldSpec{
		field("target_tenant_id", model.KindUUID, false),
		field("handle_jti", model.KindUUID, false),
		field("pep_service_id", model.KindUUID, false),
		field("nonce_hash", model.KindText, false),
		field("request_fingerprint", model.KindText, false),
		field("request_issued_at", model.KindTimestamp, false),
		indexedField("state", model.KindText, false),
		field("verdict_json", model.KindJSON, true),
		field("verdict_hash", model.KindText, true),
		field("capability_version", model.KindInt, true),
		field("effective_capabilities", model.KindJSON, true),
		field("policy_version", model.KindText, true),
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

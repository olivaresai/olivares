// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "github.com/olivaresai/olivares/core/model"

var delegationHandleDescriptor = model.EntityDescriptor{
	Kind:  "core.delegation_handle",
	Table: "delegation_handles",
	Fields: []model.FieldSpec{
		field("target_tenant_id", model.KindUUID, false),
		field("selector", model.KindText, false),
		field("secret_hash", model.KindBytes, false),
		field("source_cred_kind", model.KindText, false),
		field("source_cred_id", model.KindUUID, false),
		field("subject_user_id", model.KindUUID, false),
		field("act_as_user_id", model.KindUUID, true),
		field("agent_ref", model.KindText, true),
		field("mint_role", model.KindText, false),
		field("mint_groups", model.KindJSON, true),
		field("pep_service_id", model.KindUUID, false),
		field("audience", model.KindText, false),
		field("operations", model.KindJSON, true),
		field("bound_digest", model.KindText, true),
		indexedField("expires_at", model.KindTimestamp, false),
		field("revoked_at", model.KindTimestamp, true),
	},
	Indexes: []model.IndexSpec{
		{Name: "delegation_handles_selector_uniq", Columns: []string{"tenant_id", "selector"}, Unique: true},
	},
}

var delegationHandleCodec = model.Codec[model.DelegationHandle]{
	Base: func(h *model.DelegationHandle) *model.BaseFields { return &h.BaseFields },
	Encode: func(h model.DelegationHandle) (model.Record, error) {
		mintGroups, err := encStrings(h.MintGroups)
		if err != nil {
			return nil, err
		}
		operations, err := encStrings(h.Operations)
		if err != nil {
			return nil, err
		}
		return model.Record{
			"target_tenant_id": encTenant(h.TargetTenantID),
			"selector":         h.Selector, "secret_hash": encBytes(h.SecretHash),
			"source_cred_kind": h.SourceCredKind, "source_cred_id": h.SourceCredID.String(),
			"subject_user_id": h.SubjectUserID.String(), "act_as_user_id": encOptID(h.ActAsUserID),
			"agent_ref": h.AgentRef, "mint_role": h.MintRole, "mint_groups": mintGroups,
			"pep_service_id": h.PEPServiceID.String(), "audience": h.Audience,
			"operations": operations, "bound_digest": h.BoundDigest, "expires_at": encTS(h.ExpiresAt),
			"revoked_at": encOptTS(h.RevokedAt),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.DelegationHandle, error) {
		mintGroups, err := decStrings(r, "mint_groups")
		if err != nil {
			return model.DelegationHandle{}, err
		}
		operations, err := decStrings(r, "operations")
		if err != nil {
			return model.DelegationHandle{}, err
		}
		expires, err := decTS(r, "expires_at")
		if err != nil {
			return model.DelegationHandle{}, err
		}
		revoked, err := decOptTS(r, "revoked_at")
		if err != nil {
			return model.DelegationHandle{}, err
		}
		return model.DelegationHandle{
			BaseFields: b, TargetTenantID: decTenant(r, "target_tenant_id"),
			Selector: r.String("selector"), SecretHash: r.Bytes("secret_hash"),
			SourceCredKind: r.String("source_cred_kind"), SourceCredID: decID(r, "source_cred_id"),
			SubjectUserID: decID(r, "subject_user_id"), ActAsUserID: decID(r, "act_as_user_id"),
			AgentRef: r.String("agent_ref"), MintRole: r.String("mint_role"), MintGroups: mintGroups,
			PEPServiceID: decID(r, "pep_service_id"), Audience: r.String("audience"),
			Operations: operations, BoundDigest: r.String("bound_digest"), ExpiresAt: expires,
			RevokedAt: revoked,
		}, nil
	},
}

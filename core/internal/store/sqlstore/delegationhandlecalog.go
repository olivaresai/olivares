// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "github.com/olivaresai/olivares/core/model"

var delegationHandleDescriptor = model.EntityDescriptor{
	Kind:  "core.delegation_handle",
	Table: "delegation_handles",
	Fields: []model.FieldSpec{
		pdecl(field("target_tenant_id", model.KindUUID, false),
			model.None("the governed tenant, compared with the PEP service's tenant: core/auth/delegation.go:540")),
		pdecl(field("selector", model.KindText, false),
			model.None("the handle's public lookup key: core/auth/delegation.go:520")),
		pdecl(field("secret_hash", model.KindBytes, false),
			model.None("a one-way hash of the handle secret, only compared: core/auth/delegation.go:526")),
		pdecl(field("source_cred_kind", model.KindText, false),
			model.None("the closed source-credential kind user or token: core/auth/delegation.go:684")),
		pdecl(field("source_cred_id", model.KindUUID, false),
			model.None("the source session or token row, reloaded as such: core/auth/delegation.go:686, core/auth/delegation.go:704")),
		// The subject and the act-as account become the principal the verified
		// handle authorizes as.
		pdecl(field("subject_user_id", model.KindUUID, false), model.Ref(model.EncodeUserID, model.ClassAuthority)),
		pdecl(field("act_as_user_id", model.KindUUID, true), model.Ref(model.EncodeUserID, model.ClassAuthority)),
		pdecl(field("agent_ref", model.KindText, true),
			model.None("an agent reference, resolved by the agent lifecycle checker: core/auth/delegation.go:761")),
		pdecl(field("mint_role", model.KindText, false),
			model.None("a role ceiling from the closed role set: core/auth/delegation.go:766, core/auth/permission.go:40")),
		pdecl(field("mint_groups", model.KindJSON, true),
			pdeclStrings("user-group ids, intersected with the subject's current group closure: core/auth/delegation.go:769, core/auth/authenticator.go:454")),
		pdecl(field("pep_service_id", model.KindUUID, false),
			model.None("the PEP service row, compared with the authenticated service: core/auth/delegation.go:540")),
		pdecl(field("audience", model.KindText, false),
			model.None("a PDP audience, compared with the service's audience: core/auth/delegation.go:552")),
		pdecl(field("operations", model.KindJSON, true),
			pdeclStrings("operation kinds from a closed set: core/auth/delegation.go:1143, core/auth/delegation.go:562")),
		pdecl(field("bound_digest", model.KindText, true),
			model.None("a content digest, compared with the request's digest: core/auth/delegation.go:566")),
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

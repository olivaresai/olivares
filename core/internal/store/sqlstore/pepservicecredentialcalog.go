// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "github.com/olivaresai/olivares/core/model"

var pepServiceCredentialDescriptor = model.EntityDescriptor{
	Kind:  "core.pep_service_credential",
	Table: "pep_service_credentials",
	Fields: []model.FieldSpec{
		pdecl(field("service_id", model.KindUUID, false),
			model.None("the PEP service row the credential authenticates as: core/auth/pepservice.go:380")),
		// token_id names an API-token row, not an account. The account that owns
		// the token is read from that row (core/auth/pepservice.go:398), whose
		// user_id column carries the account declaration.
		pdecl(field("token_id", model.KindUUID, false),
			model.None("an API-token row id, matched against the presented token's own id: core/auth/pepservice.go:370")),
		field("disabled_at", model.KindTimestamp, true),
	},
	Indexes: []model.IndexSpec{
		{
			Name: "pep_service_credentials_token_id_uniq",
			Columns: []string{
				"tenant_id", "token_id",
			},
			Unique: true,
		},
	},
}

var pepServiceCredentialCodec = model.Codec[model.PEPServiceCredential]{
	Base: func(c *model.PEPServiceCredential) *model.BaseFields { return &c.BaseFields },
	Encode: func(c model.PEPServiceCredential) (model.Record, error) {
		return model.Record{
			"service_id": c.ServiceID.String(), "token_id": c.TokenID.String(),
			"disabled_at": encOptTS(c.DisabledAt),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.PEPServiceCredential, error) {
		disabled, err := decOptTS(r, "disabled_at")
		if err != nil {
			return model.PEPServiceCredential{}, err
		}
		return model.PEPServiceCredential{
			BaseFields: b, ServiceID: decID(r, "service_id"), TokenID: decID(r, "token_id"),
			DisabledAt: disabled,
		}, nil
	},
}

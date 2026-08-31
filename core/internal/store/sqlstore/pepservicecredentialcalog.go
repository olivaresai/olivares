// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "github.com/olivaresai/olivares/core/model"

var pepServiceCredentialDescriptor = model.EntityDescriptor{
	Kind:  "core.pep_service_credential",
	Table: "pep_service_credentials",
	Fields: []model.FieldSpec{
		field("service_id", model.KindUUID, false),
		field("token_id", model.KindUUID, false),
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

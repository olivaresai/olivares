// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "github.com/olivaresai/olivares/core/model"

// setSeenJTIDescriptor stores the SET jti seen-set (RFC 8417 §2.2 jti).
// Append-only and expire-TTL: rows are written once (never updated or deleted)
// and cleaned up periodically once ExpiresAt passes. Keyed on (publisher_id,
// jti) per system-tenant scope, matching the UNIQUE index below.
var setSeenJTIDescriptor = model.EntityDescriptor{
	Kind:       "core.set_seen_jti",
	Table:      "set_seen_jtis",
	AppendOnly: true,
	Fields: []model.FieldSpec{
		field("jti", model.KindText, false),
		indexedField("publisher_id", model.KindText, false),
		field("expires_at", model.KindTimestamp, false),
	},
	Indexes: []model.IndexSpec{
		{Name: "set_seen_jtis_jti_pub_uniq", Columns: []string{"tenant_id", "publisher_id", "jti"}, Unique: true},
	},
}

var setSeenJTICodec = model.Codec[model.SETSeenJTI]{
	Base: func(s *model.SETSeenJTI) *model.BaseFields { return &s.BaseFields },
	Encode: func(s model.SETSeenJTI) (model.Record, error) {
		return model.Record{
			"jti": s.JTI, "publisher_id": s.PublisherID,
			"expires_at": encTS(s.ExpiresAt),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.SETSeenJTI, error) {
		exp, err := decTS(r, "expires_at")
		if err != nil {
			return model.SETSeenJTI{}, err
		}
		return model.SETSeenJTI{BaseFields: b, JTI: r.String("jti"),
			PublisherID: r.String("publisher_id"), ExpiresAt: exp}, nil
	},
}

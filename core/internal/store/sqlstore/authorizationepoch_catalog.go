// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"fmt"

	"github.com/olivaresai/olivares/core/model"
)

// authorizationEpochDescriptor is a mutable, payload-free generation fact.
// The descriptor path creates the whole table on fresh databases and through
// additive core reconciliation on databases whose frozen v2 predates it.
var authorizationEpochDescriptor = model.EntityDescriptor{
	Kind:                   model.AuthorizationEpochKind,
	Table:                  "core_authorization_epoch",
	AuthorizationFact:      true,
	AuthorizationLockOrder: 25,
	Indexes: []model.IndexSpec{{
		Name:    "core_authorization_epoch_tenant_uniq",
		Columns: []string{"tenant_id"},
		Unique:  true,
	}},
	Checks: []string{
		"id = tenant_id",
		"version >= 1",
		fmt.Sprintf("tenant_id <> '%s'", model.SystemTenantID),
	},
}

var authorizationEpochCodec = model.Codec[model.AuthorizationEpoch]{
	Base: func(e *model.AuthorizationEpoch) *model.BaseFields { return &e.BaseFields },
	Encode: func(e model.AuthorizationEpoch) (model.Record, error) {
		if err := e.Validate(); err != nil {
			return nil, err
		}
		return model.Record{}, nil
	},
	Decode: func(b model.BaseFields, _ model.Record) (model.AuthorizationEpoch, error) {
		e := model.AuthorizationEpoch{BaseFields: b}
		if err := e.Validate(); err != nil {
			return model.AuthorizationEpoch{}, err
		}
		return e, nil
	},
}

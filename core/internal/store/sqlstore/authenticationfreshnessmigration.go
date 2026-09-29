// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/model"
)

// v17 records the nullable authentication witness. Reconcile adds the column
// to both fresh and upgraded stores; no historical session is backfilled.
// v16 stays permanently unregistered; future work must append a higher ordinal.
const coreAuthenticationFreshnessMigrationVersion = 17

func coreAuthenticationFreshnessMigration() migrate.Migration {
	return migrate.Migration{Version: coreAuthenticationFreshnessMigrationVersion,
		Name: "authentication_freshness_v1", Phase: migrate.Expand}
}

// beforeAuthenticationFreshness preserves the historical v2 statements. Its
// descriptor is copied: removing this column must not alter current reconcile.
func beforeAuthenticationFreshness(d model.EntityDescriptor) model.EntityDescriptor {
	if d.Kind != authSessionDescriptor.Kind {
		return d
	}
	fields := make([]model.FieldSpec, 0, len(d.Fields))
	for _, f := range d.Fields {
		if f.Name != "aal_authenticated_at" {
			fields = append(fields, f)
		}
	}
	d.Fields = fields
	return d
}

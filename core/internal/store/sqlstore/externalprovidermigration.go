// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "github.com/olivaresai/olivares/core/model"

// beforeExternalProvider preserves the historical federation table render.
// The current descriptor retains these nullable columns so additive reconcile
// adds them to fresh and upgraded stores without changing historical migrations.
func beforeExternalProvider(d model.EntityDescriptor) model.EntityDescriptor {
	if d.Kind != federationConfigDescriptor.Kind {
		return d
	}
	fields := make([]model.FieldSpec, 0, len(d.Fields))
	for _, f := range d.Fields {
		switch f.Name {
		case "external_connector_ref", "external_connector_generation", "external_issuer":
		default:
			fields = append(fields, f)
		}
	}
	d.Fields = fields
	return d
}

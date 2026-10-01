// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "github.com/olivaresai/olivares/core/model"

// The group-origin column (generic seam): user_groups.provisioned_by, a
// nullable TEXT recording who provisions each row (""/NULL = today's
// SCIM/IdP-managed group, "operator" = console-managed, a slug = the
// provisioner that owns it). No migration version is consumed: the column is
// appended LAST and the additive reconcile issues the ALTER TABLE ADD COLUMN
// on an existing store while v2 regenerates the table whole on a fresh one —
// the S256 parent_group_id discipline.

// beforeGroupOrigin returns d as v2 rendered it before the group-origin seam:
// without the provisioned_by column the reconcile appends. The historical DDL
// stays byte-stable; the CURRENT descriptor keeps the column for the reconcile.
func beforeGroupOrigin(d model.EntityDescriptor) model.EntityDescriptor {
	if d.Kind != userGroupDescriptor.Kind {
		return d
	}
	fields := make([]model.FieldSpec, 0, len(d.Fields))
	for _, f := range d.Fields {
		if f.Name != "provisioned_by" {
			fields = append(fields, f)
		}
	}
	d.Fields = fields
	return d
}

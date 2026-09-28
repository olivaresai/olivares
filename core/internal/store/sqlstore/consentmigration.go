// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"slices"

	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/model"
)

// Core v14: consent to join. Expand, forward only.
//
// The columns and tables it covers — the account's credential custody, a
// session's tenant scope, the tenant exclusions that are also retirement records,
// and the offers to existing accounts — are additive, so the reconcile creates
// them from their descriptors on every database, fresh or upgraded. v2 keeps
// rendering the relations exactly as it did before v14 (consentCustodyRelation,
// beforeConsentCustody), so its statements never change. What this version adds
// is the record itself: a binary that predates
// them refuses to start on a store that holds them (preflightCoreMigrationVersion),
// because it would read an account's custody as legacy, a scoped session as an
// account-wide one and an excluded member as unexcluded. v12 stays reserved.
const (
	coreConsentCustodyMigrationVersion = 14
	coreConsentCustodyMigrationName    = "consent_custody_v1"
)

// coreConsentCustodyMigration builds the v14 record. It executes no statement.
func coreConsentCustodyMigration() migrate.Migration {
	return migrate.Migration{
		Version: coreConsentCustodyMigrationVersion,
		Name:    coreConsentCustodyMigrationName,
		Phase:   migrate.Expand,
	}
}

// consentCustodyRelation reports whether kind is a relation v14 adds whole: v2
// never renders it, and the reconcile creates it.
func consentCustodyRelation(kind model.Kind) bool {
	return kind == tenantExclusionDescriptor.Kind || kind == accountOfferDescriptor.Kind
}

// beforeConsentCustody returns d as v2 rendered it before v14: without the
// columns v14 appended to the users and auth sessions relations, which the
// reconcile adds. Any other descriptor is returned unchanged.
func beforeConsentCustody(d model.EntityDescriptor) model.EntityDescriptor {
	var appended []string
	switch d.Kind {
	case userDescriptor.Kind:
		appended = []string{"credential_custody", "custody_tenant_id"}
	case authSessionDescriptor.Kind:
		appended = []string{"tenant_scope"}
	default:
		return d
	}
	fields := make([]model.FieldSpec, 0, len(d.Fields))
	for _, f := range d.Fields {
		if !slices.Contains(appended, f.Name) {
			fields = append(fields, f)
		}
	}
	d.Fields = fields
	return d
}

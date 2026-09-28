// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "github.com/olivaresai/olivares/core/model"

// secretEntryDescriptor stores the RUNTIME SECRET STORE: named secrets
// whose value is SEALED at rest. Like the auth catalog entities it is a core
// entity the engine generates and guards, it lives in the system tenant
// (BaseFields.TenantID == SystemTenantID), and it is reached ONLY through the
// auth partition (AuthScope.Secrets) — so a module, holding no Store, can never
// read a secret value. The scope column is the global-vs-per-tenant axis
// stores/resolves the single global scope (SystemTenantID).
//
// value_sealed holds the engine Sealer output (never cleartext, never a one-way
// hash). value_hint is a non-secret fingerprint prefix for display. The unique
// index leads with tenant_id (the system-tenant isolation rule) and pairs scope
// with name, so a secret name is unique within its scope.
var secretEntryDescriptor = model.EntityDescriptor{
	Kind:  "core.secret_entry",
	Table: "secret_entries",
	Fields: []model.FieldSpec{
		pdecl(indexedField("scope", model.KindUUID, false),
			model.None("the tenant scope the value is sealed under: core/auth/secret_store.go:182, core/auth/secret_store.go:258")),
		pdecl(indexedField("name", model.KindText, false),
			model.None("a secret name checked by the name validator: core/auth/secret_store.go:174")),
		pdecl(field("value_sealed", model.KindText, false),
			model.None("sealed ciphertext, produced and opened only by the sealer: core/auth/secret_store.go:189, core/auth/secret_store.go:258")),
		pdecl(field("value_hint", model.KindText, true),
			model.None("a fingerprint of the value, rendered only: core/auth/secret_store.go:193, core/api/handlers_secrets.go:53")),
		pdecl(field("description", model.KindText, true),
			model.None("operator prose, rendered only: core/api/handlers_secrets.go:53")),
	},
	Indexes: []model.IndexSpec{
		{Name: "secret_entries_scope_name_uniq", Columns: []string{"tenant_id", "scope", "name"}, Unique: true},
	},
}

var secretEntryCodec = model.Codec[model.SecretEntry]{
	Base: func(e *model.SecretEntry) *model.BaseFields { return &e.BaseFields },
	Encode: func(e model.SecretEntry) (model.Record, error) {
		return model.Record{
			"scope": encTenant(e.Scope), "name": e.Name,
			"value_sealed": e.ValueSealed, "value_hint": e.Hint, "description": e.Description,
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.SecretEntry, error) {
		return model.SecretEntry{BaseFields: b, Scope: decTenant(r, "scope"), Name: r.String("name"),
			ValueSealed: r.String("value_sealed"), Hint: r.String("value_hint"),
			Description: r.String("description")}, nil
	},
}

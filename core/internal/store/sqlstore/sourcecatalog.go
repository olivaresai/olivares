// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"encoding/json"

	"github.com/olivaresai/olivares/core/model"
)

// sourceDefDescriptor stores the DURABLE SOURCE ROSTER: one row per
// operator-defined observation source. Like the secret store and the federation
// config it is a core entity the engine generates and guards, it lives in the
// system tenant (BaseFields.TenantID == SystemTenantID), and it is reached ONLY
// through the auth partition (AuthScope.Sources) — so a module, holding no Store,
// can never read the source roster. The scope column is the global-vs-per-tenant
// axis reconciles the single global scope (SystemTenantID).
//
// config and plugin are JSON columns (deterministic text on both engines). config
// holds connector settings + secret REFERENCES (never values); plugin is the
// external-binary trust spec or NULL for a first-party kind. The unique index
// leads with tenant_id (the system-tenant isolation rule) and pairs scope with
// name, so a source name is unique within its scope.
var sourceDefDescriptor = model.EntityDescriptor{
	Kind:  "core.source_def",
	Table: "source_defs",
	Fields: []model.FieldSpec{
		pdecl(indexedField("scope", model.KindUUID, false),
			model.None("the configuration scope tenant the roster is loaded under: core/auth/source_store.go:169")),
		pdecl(indexedField("name", model.KindText, false),
			model.None("an operator handle checked by the name validator: core/auth/source_store.go:112")),
		pdecl(field("kind", model.KindText, true),
			model.None("a connector kind that selects the connector to run: cmd/olivares/reconcile.go:458, cmd/olivares/reconcile.go:476")),
		pdecl(field("tenant", model.KindText, false),
			model.None("the business tenant observations are stamped with: core/auth/source_store.go:115, cmd/olivares/reconcile.go:420")),
		field("poll_seconds", model.KindInt, false),
		field("enabled", model.KindBool, false),
		pdecl(field("config", model.KindJSON, true), model.Nested(map[string]string(nil), model.ClassEvidence,
			model.Leaf("{key}", pdeclNoneSourceConfig),
			model.Leaf("{}", pdeclNoneSourceConfig))),
		pdecl(field("plugin", model.KindJSON, true), model.Nested(model.SourcePluginRef{}, model.ClassEvidence,
			model.Leaf("path", pdeclNoneSourcePlugin),
			model.Leaf("sha256", pdeclNoneSourcePlugin),
			model.Leaf("bundle", pdeclNoneSourcePlugin),
			model.Leaf("predicate_types[]", pdeclNoneSourcePlugin))),
	},
	Indexes: []model.IndexSpec{
		{Name: "source_defs_scope_name_uniq", Columns: []string{"tenant_id", "scope", "name"}, Unique: true},
	},
}

// pdeclNoneSourceConfig and pdeclNoneSourcePlugin are why no reader resolves a
// source's connector settings or plugin pin to a principal.
var (
	pdeclNoneSourceConfig = model.None("connector settings and secret references, validated as references and handed to the connector only: core/auth/source_store.go:129, cmd/olivares/reconcile.go:439")
	pdeclNoneSourcePlugin = model.None("a plugin binary path, digest pin, attestation path and predicate allow-list, used only to admit the binary: cmd/olivares/reconcile.go:442, cmd/olivares/reconcile.go:443")
)

var sourceDefCodec = model.Codec[model.SourceDef]{
	Base: func(e *model.SourceDef) *model.BaseFields { return &e.BaseFields },
	Encode: func(e model.SourceDef) (model.Record, error) {
		rec := model.Record{
			"scope": encTenant(e.Scope), "name": e.Name, "kind": e.Kind,
			"tenant": e.Tenant, "poll_seconds": int64(e.PollSeconds), "enabled": e.Enabled,
			"config": nil, "plugin": nil,
		}
		if len(e.Config) > 0 {
			b, err := json.Marshal(e.Config)
			if err != nil {
				return nil, err
			}
			rec["config"] = string(b)
		}
		if e.Plugin != nil {
			b, err := json.Marshal(e.Plugin)
			if err != nil {
				return nil, err
			}
			rec["plugin"] = string(b)
		}
		return rec, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.SourceDef, error) {
		out := model.SourceDef{
			BaseFields: b, Scope: decTenant(r, "scope"), Name: r.String("name"),
			Kind: r.String("kind"), Tenant: r.String("tenant"),
			PollSeconds: int(r.Int("poll_seconds")), Enabled: r.Bool("enabled"),
		}
		if s := r.String("config"); s != "" {
			if err := json.Unmarshal([]byte(s), &out.Config); err != nil {
				return model.SourceDef{}, err
			}
		}
		if s := r.String("plugin"); s != "" {
			var p model.SourcePluginRef
			if err := json.Unmarshal([]byte(s), &p); err != nil {
				return model.SourceDef{}, err
			}
			out.Plugin = &p
		}
		return out, nil
	},
}

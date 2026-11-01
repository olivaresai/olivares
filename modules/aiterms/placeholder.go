// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package aiterms owns the shared AI terms schema. Business adds the behavior in
// enterprise/aiterms; Community mounts this module without routes or permissions so
// an in-place edition swap sees the same tables.
package aiterms

import (
	"context"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

const (
	// PolicyKind identifies the append-only AI terms policy records.
	PolicyKind model.Kind = "aiterms.policy"
	// AcceptanceKind identifies the append-only AI terms acceptance records.
	AcceptanceKind model.Kind = "aiterms.acceptance"
)

// Placeholder registers the shared AI terms schema without exposing Business behavior.
type Placeholder struct{}

// New returns the schema-only AI terms module.
func New() *Placeholder { return &Placeholder{} }

var _ api.Module = (*Placeholder)(nil)
var _ sdk.Module = (*Placeholder)(nil)

// Descriptor describes the AI terms module in every edition.
func (*Placeholder) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "olivares.aiterms", Version: "0.1.0", APIVersion: sdk.APIVersion, Type: sdk.TypeModule, Title: "AI terms of use", Description: "Versioned organization terms and explicit user acceptance."}
}

// Init is a no-op because the schema placeholder has no runtime dependencies.
func (*Placeholder) Init(context.Context, sdk.Host) error { return nil }

// Start is a no-op because the schema placeholder starts no workers.
func (*Placeholder) Start(context.Context) error { return nil }

// Stop is a no-op because the schema placeholder owns no resources.
func (*Placeholder) Stop(context.Context) error { return nil }

// APINamespace returns the AI terms store and API namespace.
func (*Placeholder) APINamespace() string { return "aiterms" }

// Permissions returns nil because Community exposes no AI terms operations.
func (*Placeholder) Permissions() []auth.Permission { return nil }

// APIRoutes mounts no routes; Business owns the AI terms handlers.
func (*Placeholder) APIRoutes(api.RouteRegistrar) {}

// RegisterSchema registers the AI terms policy and acceptance tables in every edition.
func (*Placeholder) RegisterSchema(reg store.ExtensionRegistry) error {
	return RegisterSchema(reg)
}

// RegisterSchema registers the AI terms policy and acceptance tables in every edition.
func RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{Kind: PolicyKind, Table: "aiterms_policy", AppendOnly: true, Fields: []model.FieldSpec{
		{Name: "policy_version", Kind: model.KindText, Principal: model.None("server generated terms version; reader enterprise/aiterms/terms.go:78")},
		{Name: "text", Kind: model.KindText, Principal: model.None("organization-authored terms, rendered as plain text; reader enterprise/aiterms/terms.go:78")},
		{Name: "digest", Kind: model.KindText, Principal: model.None("SHA-256 of the terms text; reader enterprise/aiterms/terms.go:78")},
		{Name: "enabled", Kind: model.KindBool},
		{Name: "audit_seq", Kind: model.KindInt, Indexed: true},
	}}); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{Kind: AcceptanceKind, Table: "aiterms_acceptance", AppendOnly: true, Fields: []model.FieldSpec{
		{Name: "actor", Kind: model.KindText, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
		{Name: "policy_version", Kind: model.KindText, Principal: model.None("accepted server-generated terms version; reader enterprise/aiterms/terms.go:221")},
		{Name: "digest", Kind: model.KindText, Principal: model.None("accepted text SHA-256; reader enterprise/aiterms/terms.go:221")},
		{Name: "audit_seq", Kind: model.KindInt, Indexed: true},
	}, Indexes: []model.IndexSpec{{Name: "aiterms_acceptance_unique", Columns: []string{model.ColTenantID, "actor", "policy_version"}, Unique: true}}})
}

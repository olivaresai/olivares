// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package agentroles owns the shared agent role schema. Business adds the behavior in
// enterprise/agentroles; Community mounts this module without routes or permissions so
// an in-place edition swap sees the same tables.
package agentroles

import (
	"context"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// RoleKind identifies the stored agent role records.
const RoleKind model.Kind = "agentroles.role"

// Placeholder registers the shared agent role schema without exposing Business behavior.
type Placeholder struct{}

// New returns the schema-only agent roles module.
func New() *Placeholder { return &Placeholder{} }

var _ api.Module = (*Placeholder)(nil)
var _ sdk.Module = (*Placeholder)(nil)

// Descriptor describes the agent roles module in every edition.
func (*Placeholder) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "olivares.agentroles", Version: "0.1.0", APIVersion: sdk.APIVersion, Type: sdk.TypeModule, Title: "Agent roles", Description: "Named roles for automated agents, each with its own forge identity."}
}

// Init is a no-op because the schema placeholder has no runtime dependencies.
func (*Placeholder) Init(context.Context, sdk.Host) error { return nil }

// Start is a no-op because the schema placeholder starts no workers.
func (*Placeholder) Start(context.Context) error { return nil }

// Stop is a no-op because the schema placeholder owns no resources.
func (*Placeholder) Stop(context.Context) error { return nil }

// APINamespace returns the agent roles store and API namespace.
func (*Placeholder) APINamespace() string { return "agentroles" }

// Permissions returns nil because Community exposes no agent role operations.
func (*Placeholder) Permissions() []auth.Permission { return nil }

// APIRoutes mounts no routes; Business owns the agent role handlers.
func (*Placeholder) APIRoutes(api.RouteRegistrar) {}

// RegisterSchema registers the agent role table in every edition.
func (*Placeholder) RegisterSchema(reg store.ExtensionRegistry) error {
	return RegisterSchema(reg)
}

// RegisterSchema registers the agent role table in every edition.
func RegisterSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{Kind: RoleKind, Table: "agentroles_role", Fields: []model.FieldSpec{
		{Name: "name", Kind: model.KindText, Principal: model.None("role slug chosen by an administrator, never a person (namePattern): enterprise/agentroles/roles.go:140")},
		{Name: "role_kind", Kind: model.KindText, Principal: model.None("writer or reviewer, never a person: enterprise/agentroles/roles.go:140")},
		{Name: "prompt", Kind: model.KindText, Principal: model.None("administrator-authored role prompt, plain text: enterprise/agentroles/roles.go:140")},
		{Name: "state", Kind: model.KindText, Principal: model.None("the role's own short state note, plain text: enterprise/agentroles/roles.go:140")},
		{Name: "forge_binding", Kind: model.KindText, Principal: model.None("id of a git-host credential binding of the publication custody, not a secret: enterprise/agentroles/roles.go:141")},
		{Name: "provider_profile", Kind: model.KindText, Nullable: true, Principal: model.None("name of a provider profile, not a person: enterprise/agentroles/roles.go:141")},
		{Name: "accountable", Kind: model.KindText, Principal: model.Ref(model.EncodeUserRef, model.ClassEvidence)},
		{Name: "audit_seq", Kind: model.KindInt, Indexed: true},
	}, Indexes: []model.IndexSpec{
		{Name: "agentroles_role_name_uniq", Columns: []string{model.ColTenantID, "name"}, Unique: true},
		{Name: "agentroles_role_forge_uniq", Columns: []string{model.ColTenantID, "forge_binding"}, Unique: true},
	}})
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package siemforward

import (
	"context"
	"log/slog"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/eventing"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the module's globally unique identifier.
const Name = "olivares.siemforward"

// Namespace is the module's store and API namespace.
const Namespace = "siemforward"

// auditSource is the eventing source stamped on forwarded ledger records; a ledger
// sink subscription matches it (or matches any source with an empty filter).
const auditSource = "olivares.audit"

// Entity: the per-tenant forward cursor — the highest ledger Seq already enqueued
// for SIEM forwarding. It is the at-least-once anchor: a crash or restart resumes
// the walk from this seq, and IngestAudit dedups any record re-walked.
const (
	cursorKind          model.Kind = "siemforward.cursor"
	cursorTable                    = "siemforward_cursor"
	colCurLastForwarded            = "last_forwarded_seq"
)

// Module is the SIEM-forward driver. It owns the per-tenant ledger-forward
// cursor and drives ForwardDue (the leader-gated pump calls it); it holds the
// eventing module so it can hand each sealed ledger record to the durable engine via
// IngestAudit. It registers no API routes — forwarding is internal, not a tenant
// self-service surface (a tenant configures WHERE the ledger goes through an
// eventing audit.recorded sink subscription).
type Module struct {
	log  *slog.Logger
	data api.ModuleData
	evt  *eventing.Module
}

// Compile-time proofs.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns the SIEM-forward module bound to the eventing engine it feeds.
func New(evt *eventing.Module) *Module { return &Module{evt: evt} }

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "SIEM ledger forwarder",
		Description: "Retains the per-tenant audit ledger forward cursor. Delivery of sealed records to SIEM control towers over the durable eventing platform requires Business.",
	}
}

// UseData receives the tenant-parameterized data handle before Start.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// Init keeps the host logger; it subscribes to nothing (the ledger is not on the
// bus — it is walked) and must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	return nil
}

// Start warns if the engine is unwired (no forwarding can happen), then returns —
// the actual work is driven by the composition-root pump (leader-gated).
func (m *Module) Start(context.Context) error {
	if m.log != nil && m.evt == nil {
		m.log.Warn("siemforward: no eventing engine wired; the ledger will not be forwarded")
	}
	return nil
}

// Stop is a no-op (no owned goroutines).
func (m *Module) Stop(context.Context) error { return nil }

// APINamespace roots the (empty) route set and the store namespace.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares none: the module exposes no routes.
func (m *Module) Permissions() []auth.Permission { return nil }

// APIRoutes mounts nothing: forwarding is pump-driven, not a tenant API surface.
func (m *Module) APIRoutes(api.RouteRegistrar) {}

// RegisterSchema declares the per-tenant forward cursor.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  cursorKind,
		Table: cursorTable,
		Fields: []model.FieldSpec{
			{Name: colCurLastForwarded, Kind: model.KindInt},
		},
		Indexes: []model.IndexSpec{{
			Name:    "siemforward_cursor_uniq",
			Columns: []string{model.ColTenantID},
			Unique:  true,
		}},
	})
}

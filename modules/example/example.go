// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package example is a reference Module. It subscribes to edge-observed events,
// counts them, and registers a demo data-model entity. It is the minimal,
// correct model for module authors (sessions): how to wire to host
// services in Init, how to react to events, and how to declare an entity through
// the engine-side schema seam. Modules are AGPL and may import the engine.
package example

import (
	"context"
	"log/slog"
	"sync"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// Name is the module's globally unique identifier.
const Name = "olivares.example-module"

// Module counts the access edges the engine observes and declares a demo entity.
// A real module would persist or analyze the edges; the lifecycle and the seams
// it uses are identical.
type Module struct {
	mu     sync.Mutex
	count  int
	cancel func()
	log    *slog.Logger
}

// Compile-time proof that Module satisfies the SDK contract. It also satisfies
// the engine-side runtime.SchemaProvider by implementing RegisterSchema below;
// that seam is intentionally structural so this reference module need not import
// the runtime package.
var _ sdk.Module = (*Module)(nil)

// New returns a reference module.
func New() *Module { return &Module{} }

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Example module",
		Description: "Counts observed access edges; a worked example for module authors.",
	}
}

// Init wires the module to host services and subscribes to the events it cares
// about. It must not block or start background loops (that is Start's job); here
// the work happens in the subscription handler the bus drives.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	cancel, err := host.Subscribe([]event.Type{event.TypeEdgeObserved}, m.onEdge)
	if err != nil {
		return err
	}
	m.cancel = cancel
	return nil
}

func (m *Module) onEdge(_ context.Context, e event.Event) error {
	edge, ok := event.EdgeOf(e)
	if !ok {
		return nil
	}
	m.mu.Lock()
	m.count++
	n := m.count
	m.mu.Unlock()
	if m.log != nil {
		m.log.Debug("example: observed edge", "resource", edge.ResourceRef, "mode", string(edge.Mode), "total", n)
	}
	return nil
}

// Start begins active work. This module's work is event-driven, so there is
// nothing to start; a module with background loops would launch them here.
func (m *Module) Start(context.Context) error { return nil }

// Stop releases resources. Unsubscribing here is belt-and-braces: the runtime
// also releases a module's subscriptions when it stops the module.
func (m *Module) Stop(context.Context) error {
	if m.cancel != nil {
		m.cancel()
	}
	return nil
}

// Count returns how many edges the module has observed so far.
func (m *Module) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count
}

// RegisterSchema declares the module's own data-model entity. It satisfies the
// engine-side runtime.SchemaProvider seam and is called once, at store-build
// time, before any Scope exists (store contract §6). The engine creates the
// table, injects the base columns and attaches the tenant/audit guards — a
// module cannot opt out of isolation.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:    "example.observation",
		Table:   "example_observation",
		Audited: true,
		Fields: []model.FieldSpec{
			{Name: "resource", Kind: model.KindText, Indexed: true},
			{Name: "mode", Kind: model.KindText},
		},
	})
}

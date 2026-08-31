// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"context"
	"log/slog"

	"github.com/olivaresai/olivares/sdk/event"
)

// Host is the set of engine services a Module receives at Init. It is
// deliberately narrow: the event bus, a logger and the module's configuration.
//
// It does NOT expose the data store. That is permanent and by license
// construction, not an omission: the store's Scope is an AGPL engine type
// (core/store), and this SDK is Apache-2.0 and must never import the engine.
// An in-process module that needs persistence is an AGPL component in /modules
// and receives a tenant-scoped data handle from the engine through an
// engine-side seam (see the module-authoring guide); that data path is
// standardized in the API session, not here. The event bus is the decoupled
// integration backbone every module shares.
type Host interface {
	// Publish emits an event onto the bus for other modules and output
	// connectors. It returns when the event has been accepted for delivery.
	Publish(ctx context.Context, e event.Event) error
	// Subscribe registers h to receive events whose Type is in types; an empty
	// or nil types means every event. The returned cancel function unsubscribes
	// and is safe to call once; it is also called for the module automatically
	// when the engine stops the module.
	Subscribe(types []event.Type, h event.Handler) (cancel func(), err error)
	// Logger returns a structured logger already scoped to the module (its name
	// is attached), so module logs are attributable.
	Logger() *slog.Logger
	// Config returns the module's resolved configuration.
	Config() Config
}

// Module is the engine-side extension point: a unit of product logic that
// consumes events and implements one of the product's capabilities (inventory,
// the R/RW access map, FinOps, guardrails…; ARCHITECTURE.md, README.md). Modules are
// AGPL components in /modules; they may import the engine. This SDK defines only
// their lifecycle so the runtime can drive any module uniformly and so the
// (rare) out-of-process module has a stable contract.
//
// Lifecycle: Init (wire to host services, subscribe) → Start (begin background
// work) → Stop (drain and release). Init and Stop must not block; long-running
// work belongs on goroutines the module starts in Start and stops in Stop.
//
// Declaring data-model entities (a module's own tables) is a SEPARATE phase from
// this runtime lifecycle: it happens earlier, at store-construction time,
// against the engine's ExtensionRegistry — an AGPL type that cannot appear in
// this Apache interface. An in-process module that owns entities implements the
// engine-side SchemaProvider seam (see core/runtime); an out-of-process module
// cannot register core schema and is a bus consumer only.
type Module interface {
	// Descriptor returns the module's stable self-description.
	Descriptor() Descriptor
	// Init wires the module to host services. The module subscribes to the events
	// it cares about here and keeps the host for later Publish/Logger/Config use.
	// It must return promptly and must not start background loops.
	Init(ctx context.Context, host Host) error
	// Start begins the module's active work (background goroutines, timers). It
	// returns once the module is running.
	Start(ctx context.Context) error
	// Stop gracefully stops the module: it cancels background work, waits for it
	// to finish and releases resources. After Stop the module is not reused.
	Stop(ctx context.Context) error
}

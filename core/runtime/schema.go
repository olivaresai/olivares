// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import (
	"fmt"

	"github.com/olivaresai/olivares/core/store"
)

// SchemaProvider is implemented by an in-process module that owns data-model
// entities. It is intentionally NOT part of the Apache sdk.Module interface:
// store.ExtensionRegistry is an AGPL engine type, and the SDK must never import
// the engine. An AGPL module in /modules implements both sdk.Module (its runtime
// lifecycle) and, when it owns tables, this engine-side interface.
//
// Schema registration happens at a DIFFERENT time from the Init/Start/Stop
// lifecycle: it runs once, at store-construction time, before any Scope exists
// (store contract §6). The engine boot therefore calls RegisterSchema as the
// store's register hook, then constructs and Starts the runtime:
//
//	st, err := sqlstore.Open(ctx, cfg, rt.RegisterSchema)
//	...
//	rt.Start(ctx)
//
// An out-of-process module cannot register core schema (an ExtensionRegistry
// cannot cross the gRPC boundary) and is a bus consumer only.
type SchemaProvider interface {
	// RegisterSchema declares the module's entities against the registry. It is
	// called once, before the store finishes building, and must be deterministic.
	RegisterSchema(reg store.ExtensionRegistry) error
}

// RegisterSchema fans the store's ExtensionRegistry out to every registered
// module that implements SchemaProvider, in deterministic registration order
// (requires a deterministic Register order). It is the function the engine
// boot passes to sqlstore.Open. A module's registration error is wrapped with
// its name and aborts startup — a schema that will not build is a boot failure,
// not something to isolate.
func (r *Runtime) RegisterSchema(reg store.ExtensionRegistry) error {
	r.mu.Lock()
	modules := append([]*moduleReg(nil), r.modules...)
	r.mu.Unlock()

	for _, m := range modules {
		sp, ok := m.mod.(SchemaProvider)
		if !ok {
			continue
		}
		if err := sp.RegisterSchema(reg); err != nil {
			return fmt.Errorf("runtime: module %q schema registration: %w", m.name, err)
		}
	}
	return nil
}

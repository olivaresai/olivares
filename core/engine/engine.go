// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package engine is the public seam through which a composition root (the
// engine boot, or a module's integration test) constructs a store.Store. The
// concrete dual-engine implementation lives in an internal package so a module
// can never fabricate a Store on its own — it receives a Scope/ModuleData — but
// the open-core model requires that module authors in /modules be able to wire a
// real store in their tests, and that a composition root outside /core be able
// to build one without reaching into an internal package. This package is that
// single supported constructor; it adds no behavior, only visibility.
package engine

import (
	"context"

	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/store"
)

// Open constructs a Store for cfg, fanning each module's schema out through
// register at construction time (the S02 §7 / schema seam: it runs once,
// before any Scope exists). A nil register builds the core schema only.
//
// It is a thin, behavior-free re-export of the internal implementation. The boot
// uses it (or the internal constructor directly); module integration tests use
// it to exercise their persistence against the real dual-engine store rather
// than a fake, which is what makes a module's discovery/idempotency/staleness
// coverage meaningful.
func Open(ctx context.Context, cfg store.Config, register func(store.ExtensionRegistry) error) (store.Store, error) {
	return sqlstore.Open(ctx, cfg, register)
}

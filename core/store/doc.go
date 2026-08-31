// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package store is the repository contract of the Olivares AI control plane: the
// interfaces through which the API and the 28 modules read and write the
// data model, plus the extension registry by which a module contributes its own
// entities. It is AGPL-3.0-only and depends only on core/model.
//
// The package is interfaces only — there is no Open here, and no method ever
// returns a *sql.DB/*sql.Tx or takes a tenant id as a free parameter. A caller
// binds a tenant once (Store.View/Store.Mutate) and receives a Scope whose
// every repository is already pinned to that tenant; cross-tenant access is not
// merely discouraged, it is absent from the vocabulary (ARCHITECTURE.md, docs/SECURITY-HARDENING.md).
// The concrete store is constructed by the engine itself (internal/store), so a
// module receives a Scope and can never reach the unscoped machinery.
package store

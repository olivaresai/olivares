// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package model is the persistence data model of the Olivares AI control plane:
// the entity types (ARCHITECTURE.md), the value objects (ID, TenantID, Timestamp,
// Kind), and the descriptor/codec contract by which a module registers its own
// entities without touching the core (ARCHITECTURE.md, "extensible").
//
// It is the contract that the store, the API and the 28 modules code
// against. It is AGPL-3.0-only and depends only on the standard library, the
// well-audited google/uuid generator and the Apache SDK wire vocabulary
// (sdk/model) for the shared R/RW enums. It contains no database code: a model
// type never touches a connection. Connectors cannot import this package (the
// license frontier and the import graph both forbid it); they speak the wire
// vocabulary in sdk/model and the engine maps it here.
package model

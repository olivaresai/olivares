// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package modules is the root of the product modules (inventory, the R/RW
// access map, FinOps, evals, guardrails, compliance…; see README.md and this
// directory's README for the authoritative list). Each module
// lives in its own subpackage, consumes normalized data from core and the SDK,
// and registers its own entities, API endpoints and UI views without modifying
// core or sibling modules.
//
// Empty at bootstrap; modules are added from session onward.
package modules

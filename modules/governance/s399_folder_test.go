// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import ()

// folder-scoped delegated administration, end to end: a folder-scoped grant
// authored via REST enforces on the real path with downward subtree inheritance, the
// delegation ceiling confines a folder-admin to its subtree, and the catalog advertises the
// new tree.

// Downward-only inheritance is the whole security property of folder scope: a folder grant
// reaches DESCENDANTS but never an ANCESTOR or a SIBLING subtree (asserted at ENFORCEMENT, not
// just the delegation ceiling).

// A folder-admin may sub-delegate ONLY within its subtree — never a sibling, an ancestor, or
// a broader tenant scope (no upward escalation).

// A workspace-admin may NOT delegate a folder grant at all (adversarial-review fix): a
// Resource's workspace_id is decoupled from its tree position, so a folder anchored in the
// admin's workspace could enclose descendants in OTHER workspaces and the folder permit
// carries no workspace bound — allowing the delegation would be a cross-workspace escape.
// Only a tenant admin or a folder admin may delegate folders.

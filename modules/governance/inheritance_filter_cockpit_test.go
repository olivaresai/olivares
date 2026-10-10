// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import ()

// COCKPIT-02 §6 point 2: "only explicit grants when confined" was inexpressible, because
// RequireScopedGrant is one bool of the ROUTE. One filter row on the department's node
// (workspace, class session) now says it: rights that reach a session in that workspace from
// ABOVE (a role, a tenant-wide grant) no longer carry a read, and a grant at or below the
// node still does. The department forbid-unless of point 1 is unchanged and still decides on
// the full graph, so both controls compose as the contract requires.

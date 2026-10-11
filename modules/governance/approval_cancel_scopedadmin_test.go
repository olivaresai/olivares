// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import ()

// A principal that holds the approval admin permission only through a tenant-scope
// grant (its membership role is viewer) may cancel another requester's request. The
// route admits it at approval:write; the cancel handler's own question must go to the
// admission seam, not to the rank of the membership role.

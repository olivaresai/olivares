// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import "github.com/olivaresai/olivares/sdk/model"

// modeFromAnnotations derives an R/RW mode hint from a tool's UNTRUSTED
// annotations, honoring the MCP spec's asymmetric defaults: readOnlyHint defaults
// false and destructiveHint defaults true, so a tool is assumed to modify its
// environment unless it explicitly declares readOnlyHint=true. The result is
// always a hint — the caller stamps it ConfidenceApproximate and
// SignalMCPAnnotation so no consumer mistakes it for an observed access.
//
//   - readOnlyHint == true            → read (the only "does not modify" claim)
//   - readOnlyHint == false / absent  → readwrite (modifies; a tool also reads its
//     inputs, so readwrite is more faithful than write-only)
func modeFromAnnotations(a *ToolAnnotations) model.AccessMode {
	if a != nil && a.ReadOnlyHint != nil && *a.ReadOnlyHint {
		return model.ModeRead
	}
	return model.ModeReadWrite
}

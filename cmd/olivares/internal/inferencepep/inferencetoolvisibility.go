// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferencepep

import "github.com/olivaresai/olivares/modules/inferenceproxy"

// Preserve an absent annotation for a session with no frozen request. Only the
// classifier's closed labels enter retained metadata; no tool data is copied.
func proxyToolVisibilityMeta(meta map[string]any, visibility inferenceproxy.ToolVisibility) map[string]any {
	switch visibility {
	case inferenceproxy.ToolVisibilityFull, inferenceproxy.ToolVisibilityPartial:
		meta["tool_visibility"] = string(visibility)
	}
	return meta
}

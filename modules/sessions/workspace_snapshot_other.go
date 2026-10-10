//go:build !linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
)

func readWorkspaceSnapshot(context.Context, *resolvedWorkspace, string) (snapshotTree, error) {
	return snapshotTree{}, &runErr{http.StatusServiceUnavailable, "workspace snapshot reading is unavailable on this platform"}
}

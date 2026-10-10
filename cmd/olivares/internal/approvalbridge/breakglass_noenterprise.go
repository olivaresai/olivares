//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package approvalbridge

import "context"

// The Community adapter preserves the gate contract without consulting emergency grants.
func (b *Bridge) breakGlassConsumeUnlessRejected(context.Context, ServiceCred, string, string, string) (string, bool) {
	return "", false
}

func (b *Bridge) breakGlassActive(context.Context, ServiceCred, string) bool {
	return false
}

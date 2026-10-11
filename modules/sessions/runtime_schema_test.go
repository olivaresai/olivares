// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "testing"

// A malformed principal declaration in any sessions descriptor prevents the
// composition from retiring accounts, even when the descriptor has no rows.
func TestSessionSchemaPrincipalDeclarationsAreComplete(t *testing.T) {
	t.Parallel()
	for _, descriptor := range communicationCaptureSchema(t).descriptors {
		for _, defect := range descriptor.PrincipalDefects() {
			t.Error(defect)
		}
	}
}

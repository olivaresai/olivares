// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !(linux && cgo && olivares_pam)

package localpam

import "github.com/olivaresai/olivares/appliance/layer/portal/auth"

// Built reports that this binary carries the adapter: it does not.
const Built = false

// Start refuses: this binary cannot reach the host's sign-in services.
func (Stack) Start(string, string) (auth.Transaction, error) {
	_ = errRefused
	_ = consoleTerminal
	return nil, ErrNotBuilt
}

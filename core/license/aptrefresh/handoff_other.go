// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !unix

package aptrefresh

import "fmt"

// publish refuses: the handoff is read by the appliance helper, which runs on Linux only.
func publish(dir, cycle string, data []byte) error {
	return fmt.Errorf("%w: the APT handoff is written on unix systems only", ErrHandoffCustody)
}

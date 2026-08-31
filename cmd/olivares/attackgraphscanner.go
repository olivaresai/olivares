// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// attack-graph scanner interface: the enterprise add-on implements
// continuous scanning, risk scoring, and exfil anomaly detection over the
// attack-path graph. The open build returns nil (no scanner, the AGPL
// attack-path queries still work). The interface is defined here
// (build-independent) so both wire files can reference it.

// attackGraphScanner is the interface for the enterprise continuous scanner.
// nil = no scanner (the open build).
//
// `unused` reports this type, and it is WRONG in the way a build-tagged seam always fools it:
// the enterprise wire file lives outside this repository and returns a real scanner through
// this interface, while the AGPL build returns nil from newAttackGraphScanner. The linter only
// ever sees the !enterprise side, so it can never observe what gives this type its purpose.
//
//nolint:unused // open-core seam: the enterprise wire file implements it
type attackGraphScanner interface {
	// Start begins the continuous scanning loop. It blocks until ctx is done.
	Start() error
	// Stop gracefully shuts down the scanner.
	Stop()
}

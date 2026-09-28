// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package repair holds the two effects the repair console on tty1 owns in its own root process,
// beside the helpers it asks: restoring the portal's sign-in stack from the package's pristine copy,
// and the durable linkage record of a recovery reboot, written before the power helper is asked.
// The console package itself writes no file; these are the only files it causes to be written
// without a helper, each under a fixed name in a protected directory, created exclusively, synced
// and never reached through a link.
package repair

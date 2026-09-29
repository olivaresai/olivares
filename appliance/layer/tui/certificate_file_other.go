// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package tui

// ReadCertificateFingerprint refuses outside the Linux appliance environment.
func ReadCertificateFingerprint(_ string) (string, bool, string) {
	return "", false, "certificate measurement requires Linux"
}

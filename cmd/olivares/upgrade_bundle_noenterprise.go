//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/release"
)

func requireBundleInstallEdition(o *upgradeOptions) error {
	if o.bundle != "" && !o.check {
		return offlineInstallUnavailable()
	}
	return nil
}

func requireBundleLicense(o *upgradeOptions, _ release.Manifest) error {
	return requireBundleInstallEdition(o)
}

func offlineInstallUnavailable() error {
	return sentence(exitcode.Edition, "Offline bundle installation is an Enterprise feature: %s. Use --bundle --check to verify without installing.", pricingURL)
}

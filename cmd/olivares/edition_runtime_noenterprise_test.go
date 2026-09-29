// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license"
)

func TestCommunityEntitlementBindingDoesNotConsultLicense(t *testing.T) {
	holder := &licenseHolder{clock: func() time.Time {
		t.Fatal("Community binder consulted the license holder")
		return time.Time{}
	}}
	bindEnterpriseEntitlement(func() ([]license.Grant, bool) {
		t.Fatal("Community binder consulted license grants")
		return nil, false
	}, holder)
	// A Community deployment without commercial inputs has the same no-op.
	bindEnterpriseEntitlement(nil, nil)
}

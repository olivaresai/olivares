// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package hostops_test

import (
	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"testing"
)

func TestLifecycle_PortalDoesNotWaitBehindExclusiveMaintenance(t *testing.T) {
	dir := t.TempDir()
	if err := hostops.InitLifecycle(dir); err != nil {
		t.Fatal(err)
	}
	initializer, err := hostops.OpenLifecycle(dir, hostops.RoleInitializer)
	if err != nil {
		t.Fatal(err)
	}
	defer initializer.Close()
	portal, err := hostops.OpenLifecycle(dir, hostops.RolePortal)
	if err != nil {
		t.Fatal(err)
	}
	defer portal.Close()
	if err := initializer.Exclusive(); err != nil {
		t.Fatal(err)
	}
	if err := portal.TryShared(); err == nil {
		t.Fatal("shared hold bypassed maintenance")
	}
	_ = initializer.Close()
	if err := portal.TryShared(); err != nil {
		t.Fatal(err)
	}
}

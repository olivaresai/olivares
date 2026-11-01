// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

func TestApplianceImageMarkerIsDistinctFromTheAccountSplitMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image")
	if isApplianceImage(path) {
		t.Fatal("missing marker enabled appliance defaults")
	}
	for _, tc := range []struct {
		body string
		want bool
	}{
		{"almalinux10-server-amd64\n", true},
		{"almalinux10-desktop-amd64\n", true},
		{"debian13-server-amd64\n", true},
		{"service_account=olivares-svc\n", false},
		{"", false}, {"unknown-image\n", false},
	} {
		if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		if got := isApplianceImage(path); got != tc.want {
			t.Fatalf("profile %q =%v want %v", tc.body, got, tc.want)
		}
	}
}

func TestConsoleDRUsesTheRecipeMarkerWithoutTheAccountSplitContract(t *testing.T) {
	original := readApplianceImage
	defer func() { readApplianceImage = original }()
	asked := ""
	readApplianceImage = func(path string) bool { asked = path; return true }
	cfg := consoleDRConfig(bootConfig{DataDir: t.TempDir()}, store.EngineSQLite, nil, nil)
	if asked != "/etc/olivares-appliance/image" || !cfg.Appliance {
		t.Fatalf("wrong image detection: path=%q appliance=%v", asked, cfg.Appliance)
	}
}

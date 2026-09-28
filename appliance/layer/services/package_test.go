// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServices_PortalPackageShipsTheUnitsHelperWithModesAndOwners(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	data, err := os.ReadFile(filepath.Join(root, "packaging", "nfpm", "olivares-appliance-portal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	var pkg struct {
		Name     string `json:"name"`
		Contents []struct {
			Src      string `json:"src"`
			Dst      string `json:"dst"`
			FileInfo struct {
				Mode  int    `json:"mode"`
				Owner string `json:"owner"`
				Group string `json:"group"`
			} `json:"file_info"`
		} `json:"contents"`
	}
	if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &pkg); err != nil || pkg.Name != "olivares-appliance-portal" {
		t.Fatalf("the portal package does not parse: %v", err)
	}
	want := map[string]struct {
		src  string
		mode int
	}{
		"/usr/libexec/olivares/olivares-portal-units":            {"bin/olivares-portal-units", 0o755},
		"/usr/lib/systemd/system/olivares-helper-units.socket":   {"appliance/layer/services/units/olivares-helper-units.socket", 0o644},
		"/usr/lib/systemd/system/olivares-helper-units@.service": {"appliance/layer/services/units/olivares-helper-units@.service", 0o644},
	}
	seen := map[string]int{}
	for _, entry := range pkg.Contents {
		if strings.HasPrefix(entry.Src, "appliance/layer/services/") || strings.Contains(entry.Dst, "units") && strings.Contains(entry.Dst, "olivares") {
			seen[entry.Dst]++
			w, ok := want[entry.Dst]
			if !ok {
				t.Errorf("the package ships %s from %s, which is not one of the units helper's files", entry.Dst, entry.Src)
				continue
			}
			if entry.Src != w.src || entry.FileInfo.Mode != w.mode || entry.FileInfo.Owner != "root" || entry.FileInfo.Group != "root" {
				t.Errorf("%s: src %s mode %o owner %q group %q, want %s %o root root", entry.Dst, entry.Src, entry.FileInfo.Mode, entry.FileInfo.Owner, entry.FileInfo.Group, w.src, w.mode)
			}
			if !strings.HasPrefix(entry.Src, "bin/") {
				if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(entry.Src))); err != nil || !info.Mode().IsRegular() {
					t.Errorf("%s: its source %s is not in the tree: %v", entry.Dst, entry.Src, err)
				}
			}
		}
	}
	for dst := range want {
		if seen[dst] != 1 {
			t.Errorf("the package ships %s %d times, want once", dst, seen[dst])
		}
	}
}

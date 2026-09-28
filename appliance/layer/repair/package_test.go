// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package repair

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packageEntry is one file the portal package installs, as its manifest states it.
type packageEntry struct {
	Src      string `json:"src"`
	Dst      string `json:"dst"`
	Packager string `json:"packager"`
	FileInfo struct {
		Mode  int    `json:"mode"`
		Owner string `json:"owner"`
		Group string `json:"group"`
	} `json:"file_info"`
}

func TestPackage_PortalShipsTheCertificateHelperThePristineStackAndTheRecoveryDirectory(t *testing.T) {
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
	var manifest struct {
		Name     string         `json:"name"`
		Contents []packageEntry `json:"contents"`
	}
	if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &manifest); err != nil || manifest.Name != "olivares-appliance-portal" {
		t.Fatalf("the portal package does not parse: %v", err)
	}
	type want struct {
		src, packager string
		mode          int
	}
	wanted := map[string][]want{
		"/usr/libexec/olivares/olivares-portal-cert":            {{"bin/olivares-portal-cert", "", 0o755}},
		"/usr/lib/systemd/system/olivares-helper-cert.socket":   {{"appliance/layer/helpers/units/olivares-helper-cert.socket", "", 0o644}},
		"/usr/lib/systemd/system/olivares-helper-cert@.service": {{"appliance/layer/helpers/units/olivares-helper-cert@.service", "", 0o644}},
		// The pristine copy is the package's own stack, byte for byte: the deb's and the rpm's.
		PristinePortalPAM: {
			{"appliance/layer/portal/units/pam.d/olivares-portal", "deb", 0o644},
			{"appliance/layer/portal/units/pam.d/rpm/olivares-portal", "rpm", 0o644},
		},
		"/usr/lib/tmpfiles.d/olivares-power.conf": {{"appliance/layer/repair/tmpfiles.d/olivares-power.conf", "", 0o644}},
	}
	for dst, entries := range wanted {
		var shipped []packageEntry
		for _, entry := range manifest.Contents {
			if entry.Dst == dst {
				shipped = append(shipped, entry)
			}
		}
		if len(shipped) != len(entries) {
			t.Errorf("the package ships %s %d times, want %d", dst, len(shipped), len(entries))
			continue
		}
		for i, w := range entries {
			got := shipped[i]
			if got.Src != w.src || got.Packager != w.packager || got.FileInfo.Mode != w.mode || got.FileInfo.Owner != "root" || got.FileInfo.Group != "root" {
				t.Errorf("%s: %+v, want src %s packager %q mode %o root:root", dst, got, w.src, w.packager, w.mode)
			}
			if !strings.HasPrefix(w.src, "bin/") {
				if info, err := os.Stat(filepath.Join(root, w.src)); err != nil || !info.Mode().IsRegular() {
					t.Errorf("%s: its source %s is not in the tree: %v", dst, w.src, err)
				}
			}
		}
	}
	if info, err := os.Stat(filepath.Join("..", "helpers", "olivares-portal-cert", "main.go")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("no program olivares-portal-cert is built from this layer: %v", err)
	}

	t.Run("the recovery directory is root's alone, on persistent host state", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("tmpfiles.d", "olivares-power.conf"))
		if err != nil {
			t.Fatal(err)
		}
		var rows []string
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				rows = append(rows, strings.Join(strings.Fields(line), " "))
			}
		}
		wantRows := []string{"d /var/lib/olivares-power 0700 root root - -", "d " + LinkageDirectory + " 0700 root root - -"}
		if strings.Join(rows, "\n") != strings.Join(wantRows, "\n") {
			t.Fatalf("the tmpfiles rows are %q, want %q", rows, wantRows)
		}
	})
}

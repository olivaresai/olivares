// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/base64"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// aptSnapshotDir records every entry under root: its mode and, for a regular file, its bytes.
func aptSnapshotDir(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry := info.Mode().String()
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry += " " + string(b)
		}
		out[rel] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestLicenseInstall_RefusesAptDownloadCredential: QD-4 (Interface Q3 r2 §3.1.1.4). A download
// credential is not a licence: `license install -` refuses an oad1. string by name, before it reads any
// trust and with --force too, and leaves the installed licence and the data directory unchanged. The
// refusal never echoes the credential.
func TestLicenseInstall_RefusesAptDownloadCredential(t *testing.T) {
	t.Setenv("OLIVARES_LICENSE", "")
	t.Setenv("OLIVARES_LICENSE_PATH", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, licenseFileName), []byte("the installed licence, kept byte for byte\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := aptSnapshotDir(t, dir)
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"s":"olivares.ai/apt-download/v1","aud":"https://licenses.olivares.ai/apt/v1/"}`))
	cred := "oad1." + payload + "." + strings.Repeat("A", 43)
	for _, input := range []string{cred, cred + "\n", "  " + cred + "\r\n"} {
		for _, args := range [][]string{{"--data-dir", dir}, {"--data-dir", dir, "--force"}} {
			out, err := installLicence(t, input, args...)
			if err == nil {
				t.Fatalf("license install %v accepted a download credential:\n%s", args, out)
			}
			if !strings.Contains(err.Error(), "APT download credential") {
				t.Errorf("license install %v: the refusal must name the APT download credential, got: %v", args, err)
			}
			if strings.Contains(out, payload) || strings.Contains(err.Error(), payload) {
				t.Errorf("license install %v echoed the credential", args)
			}
			if after := aptSnapshotDir(t, dir); !maps.Equal(after, before) {
				t.Fatalf("license install %v changed the data directory:\nbefore %v\nafter  %v", args, before, after)
			}
		}
	}
}

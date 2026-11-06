// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDRBackupSaysWhatItLeavesOut is the #3027 statement kept true now that the
// bundle carries the folders and sign-ins (#3029): `dr backup` and `dr inspect`
// name what the bundle leaves out, with the real path and the reason, and never
// name what it carries.
func TestDRBackupSaysWhatItLeavesOut(t *testing.T) {
	prevVersion := version
	version = "1.900"
	t.Cleanup(func() { version = prevVersion })
	clearSealerKeyEnv(t)
	src := t.TempDir()
	seedDataDir(t, src)
	scratch := t.TempDir()
	off := false
	registerFolder(t, src, "scratch-folder", scratch, "rw", &off)
	models := filepath.Join(src, "ollama")
	if err := os.MkdirAll(models, 0o700); err != nil {
		t.Fatal(err)
	}
	homes := filepath.Join(src, "profile-homes", "p1")
	if err := os.MkdirAll(homes, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homes, "auth.json"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	bundle := filepath.Join(t.TempDir(), "estate.drbundle")
	out, err := runDR(t, "backup", "--data-dir", src, "--out", bundle, "--passphrase-file", l3Passphrase(t))
	if err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	if !strings.Contains(out, "not included") || !strings.Contains(out, scratch) {
		t.Fatalf("dr backup must say what the bundle leaves out, folder %s included:\n%s", scratch, out)
	}

	out, err = runDR(t, "inspect", "--in", bundle)
	if err != nil {
		t.Fatalf("inspect: %v\n%s", err, out)
	}
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("inspect printed no manifest:\n%s", out)
	}
	var m struct {
		NotIncluded []struct {
			Path   string `json:"path"`
			Reason string `json:"reason"`
		} `json:"not_included"`
	}
	if err := json.NewDecoder(strings.NewReader(out[start:])).Decode(&m); err != nil {
		t.Fatalf("manifest: %v\n%s", err, out)
	}
	left := map[string]string{}
	for _, n := range m.NotIncluded {
		left[n.Path] = n.Reason
	}
	for _, want := range []string{scratch, models} {
		if left[want] == "" {
			t.Errorf("dr inspect not_included lacks %s with a reason; has %v", want, left)
		}
	}
	if _, carried := left[filepath.Join(src, "profile-homes")]; carried {
		t.Errorf("profile-homes is carried by the bundle but dr inspect lists it as left out: %v", left)
	}
}

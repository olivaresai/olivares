// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDROffsiteLiveBucket uses the disposable bucket provisioned by
// scripts/dr-offsite-targets.sh. The ordinary suite never needs a bucket.
func TestDROffsiteLiveBucket(t *testing.T) {
	endpoint := os.Getenv("OLIVARES_TEST_OFFSITE_ENDPOINT")
	if endpoint == "" {
		t.Skip("run scripts/dr-offsite-targets.sh for real-bucket qualification")
	}
	flags := []string{
		"--offsite-endpoint", endpoint,
		"--offsite-bucket", "dr-qualification",
		"--offsite-region", "us-east-1",
		"--offsite-prefix", "cli",
		"--offsite-access-key-id-file", os.Getenv("OLIVARES_TEST_OFFSITE_ACCESS_FILE"),
		"--offsite-secret-access-key-file", os.Getenv("OLIVARES_TEST_OFFSITE_SECRET_FILE"),
	}
	prevVersion := version
	version = "1.900"
	t.Cleanup(func() { version = prevVersion })
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	seedDataDir(t, src)
	pass := filepath.Join(dir, "pass")
	if err := os.WriteFile(pass, []byte("disposable DR qualification passphrase"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := func(args ...string) string {
		t.Helper()
		out, err := runDR(t, args...)
		if err != nil {
			t.Fatalf("dr %s: %v\n%s", args[0], err, out)
		}
		return out
	}
	// The first copy must be deleted by the second backup's actual GFS path.
	old := filepath.Join(dir, "a-old.drbundle")
	command(append([]string{"backup", "--data-dir", src, "--engine", "sqlite", "--out", old, "--passphrase-file", pass}, flags...)...)
	current := filepath.Join(dir, "z-current.drbundle")
	out := command(append([]string{"backup", "--data-dir", src, "--engine", "sqlite", "--out", current, "--passphrase-file", pass,
		"--gfs-keep-last", "1"}, flags...)...)
	if !strings.Contains(out, "GFS pruned offsite bundle: a-old.drbundle") {
		t.Fatalf("GFS never deleted the older real object:\n%s", out)
	}
	out = command(append([]string{"list", "--offsite"}, flags...)...)
	if !strings.Contains(out, "z-current.drbundle") || strings.Contains(out, "a-old.drbundle") {
		t.Fatalf("offsite list after GFS:\n%s", out)
	}
	if out, err := runDR(t, append([]string{"pull", "--name", "a-old.drbundle", "--out", filepath.Join(dir, "deleted")}, flags...)...); err == nil || !strings.Contains(err.Error(), "returned HTTP 404: NoSuchKey") {
		t.Fatalf("GFS-deleted object is still readable or failed for another reason: %v\n%s", err, out)
	}
	pulled := filepath.Join(dir, "pulled.drbundle")
	command(append([]string{"pull", "--name", "z-current.drbundle", "--out", pulled}, flags...)...)
	want, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(pulled)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("pulled bundle differs from the uploaded snapshot")
	}
	// Leave only the downloaded snapshot for the recovery.
	for _, path := range []string{src, old, current} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	restored := filepath.Join(dir, "restored")
	out = command("restore", "--in", pulled, "--data-dir", restored,
		"--engine", "sqlite", "--passphrase-file", pass)
	if !strings.Contains(out, "restore verified") {
		t.Fatalf("restored ledger was not verified:\n%s", out)
	}
	for _, name := range []string{"olivares.db", "audit-signing.key", "catalog-signing.key"} {
		info, err := os.Stat(filepath.Join(restored, name))
		if err != nil || info.Size() == 0 {
			t.Fatalf("restored %s missing or empty: %v", name, err)
		}
	}
	t.Log("real bucket: backup, GFS delete, list, pull, byte comparison, source destruction and verified restore passed")
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func place(t *testing.T, root, path, content string) string {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestTemplateIdentity_ACleanTemplateHasNoInstanceIdentityAndAnInitializedInstanceIsNeverATemplate(t *testing.T) {
	root := t.TempDir()
	// A clean template: package files and public defaults, no instance identity.
	place(t, root, "etc/machine-id", "uninitialized\n")
	place(t, root, "etc/ssh/ssh_config", "# public default\n")
	place(t, root, filepath.Join(ProductDataDir, "install-manifest.json"), "{}\n")
	if found, err := TemplateFindings(root); err != nil || len(found) != 0 {
		t.Fatalf("clean template reported %v %v", found, err)
	}
	place(t, root, "etc/machine-id", "")
	if found, err := TemplateFindings(root); err != nil || len(found) != 0 {
		t.Fatalf("an empty machine-id is a template form: %v %v", found, err)
	}

	// Each instance identity is found on its own, the three signing keys included.
	identities := map[string]string{
		filepath.Join(ProductDataDir, "audit-signing.key"):   "audit-signing.key",
		filepath.Join(ProductDataDir, "catalog-signing.key"): "catalog-signing.key",
		filepath.Join(ProductDataDir, "policy-signing.key"):  "policy-signing.key",
		filepath.Join(ProductDataDir, "tls.key"):             "tls.key",
		filepath.Join(ProductDataDir, "setup.token"):         "setup.token",
		filepath.Join(ProductDataDir, "olivares.db"):         "olivares.db",
		"etc/ssh/ssh_host_ed25519_key":                       "SSH host key ssh_host_ed25519_key",
		filepath.Join(StateDir, recordFile):                  "first-boot record",
	}
	for path, name := range identities {
		full := place(t, root, path, "instance\n")
		found, err := TemplateFindings(root)
		if err != nil || !slices.Equal(found, []string{name}) {
			t.Fatalf("%s: found %v %v", path, found, err)
		}
		if err := os.Remove(full); err != nil {
			t.Fatal(err)
		}
	}
	place(t, root, "etc/machine-id", "0123456789abcdef0123456789abcdef\n")
	if found, _ := TemplateFindings(root); !slices.Equal(found, []string{"machine-id"}) {
		t.Fatalf("a populated machine-id: %v", found)
	}

	// An imported initialized installation is refused before any effect, never re-identified.
	in := answersFixture(t, "olivares.example.test")
	imported := t.TempDir()
	place(t, imported, filepath.Join(ProductDataDir, "policy-signing.key"), "instance\n")
	dir, h := t.TempDir(), newFakeHost()
	m := newMachine(dir, h, &in, h.seams())
	m.Identities = func() ([]string, error) { return ProductIdentities(imported) }
	rec, err := m.Run(context.Background())
	if err != nil || rec.State != Refused || rec.Stage != StageValidate || !strings.Contains(rec.Reason, "policy-signing.key") {
		t.Fatalf("imported instance: %+v %v", rec, err)
	}
	if len(h.applies) != 0 {
		t.Fatalf("effects on an imported instance: %v", h.applies)
	}

	// Once first boot started the product, the product's identities are expected, and the
	// instance, with its record, is never a template again.
	started, hs := t.TempDir(), newFakeHost()
	first := newMachine(started, hs, &in, hs.seams())
	first.crash = func(s Stage, b boundary) bool { return s == StageStartServices && b == afterPersist }
	if _, err := first.Run(context.Background()); !errors.Is(err, errCrashed) {
		t.Fatalf("first boot: %v", err)
	}
	m = newMachine(started, hs, &in, hs.seams())
	m.Identities = func() ([]string, error) { return ProductIdentities(imported) }
	if rec, err := m.Run(context.Background()); err != nil || rec.State != Ready {
		t.Fatalf("an initialized instance's own identities were refused: %+v %v", rec, err)
	}
	instance := t.TempDir()
	place(t, instance, filepath.Join(StateDir, recordFile), "{}\n")
	if found, _ := TemplateFindings(instance); !slices.Contains(found, "first-boot record") {
		t.Fatal("an initialized instance was reported as a template")
	}
}

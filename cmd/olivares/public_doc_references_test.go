// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/ed25519"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/release"
	"github.com/spf13/cobra"
)

// The development tree contains documents the public export intentionally omits.
// Check its real manifest; in an exported checkout, check the shipped files directly.
func publicDocumentPaths(t *testing.T) map[string]bool {
	t.Helper()
	root := filepath.Clean("../..")
	paths := map[string]bool{}
	if _, err := os.Stat(filepath.Join(root, "scripts/export-public.sh")); err == nil {
		cmd := exec.Command("bash", "scripts/export-public.sh", "--manifest")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("read public export manifest: %v\n%s", err, out)
		}
		for _, p := range strings.Fields(string(out)) {
			paths[p] = true
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	} else {
		if err := filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() && entry.Name() == ".git" {
				return filepath.SkipDir
			}
			if !entry.IsDir() {
				rel, err := filepath.Rel(root, p)
				if err != nil {
					return err
				}
				paths[filepath.ToSlash(rel)] = true
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if !paths["LICENSING.md"] || !paths["docs/SECURITY-HARDENING.md"] {
		t.Fatal("public document inventory is missing the licensing or security guide")
	}
	return paths
}

var publicDocReference = regexp.MustCompile("(?:^|[\\s(`])((?:[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+)|(?:[A-Z][A-Z0-9_-]*\\.md))")

func checkPublicDocReferences(t *testing.T, paths map[string]bool, text string) {
	t.Helper()
	for _, match := range publicDocReference.FindAllStringSubmatch(text, -1) {
		target := strings.TrimRight(match[1], ".")
		if prefix, _, hasSlash := strings.Cut(target, "/"); hasSlash {
			// Relative input files and paths outside repository directories are user data.
			if prefix == "." || prefix == ".." {
				continue
			}
			info, err := os.Stat(filepath.Join("../..", prefix))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if !info.IsDir() {
				continue
			}
		}
		if paths[target] {
			continue
		}
		// A cited directory ships if the manifest includes files below it.
		found := false
		for p := range paths {
			if strings.HasPrefix(p, strings.TrimRight(target, "/")+"/") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("repository reference %q is absent from the public export", target)
		}
	}
}

func TestCLIHelpDocumentReferencesAreExported(t *testing.T) {
	paths := publicDocumentPaths(t)
	walk(t, newRootCmd(), func(path string, cmd *cobra.Command) {
		t.Run(path, func(t *testing.T) {
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			if err := cmd.Help(); err != nil {
				t.Fatal(err)
			}
			checkPublicDocReferences(t, paths, out.String())
		})
	})
}

func TestLoginGuideDocumentReferencesAreExported(t *testing.T) {
	paths := publicDocumentPaths(t)
	const guide = "docs/LOGIN-ENFORCEMENT-OPERATIONS.md"
	if !paths[guide] {
		t.Fatal("login guide is missing from the public export")
	}
	text, err := os.ReadFile(filepath.Join("../..", guide))
	if err != nil {
		t.Fatal(err)
	}
	links := regexp.MustCompile(`\]\(([^)#]+\.md)(?:#[^)]*)?\)`).FindAllStringSubmatch(string(text), -1)
	if len(links) == 0 {
		t.Fatal("login guide contains no document links")
	}
	for _, link := range links {
		target := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(guide), link[1])))
		if !paths[target] {
			t.Errorf("%s links to %q, absent from the public export", guide, target)
		}
	}
}

func TestCRLWarningDocumentReferencesAreExported(t *testing.T) {
	paths := publicDocumentPaths(t)
	t.Setenv("OLIVARES_LICENSE", "")
	t.Setenv("OLIVARES_LICENSE_PATH", "")
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeLicenseTrustDocument(dir, license.TrustDocument{Keys: []license.TrustDocumentKey{
		{PublicKey: pub, State: license.KeyStateCurrent},
	}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	blob, err := license.Sign(license.Claims{Licensee: "Documentation test", Serial: "doc-test", IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, licenseFileName), []byte(blob), 0o600); err != nil {
		t.Fatal(err)
	}
	lines := describeCRLForLicense(dir, release.Manifest{Revoked: &release.RevokedSet{Serials: []string{"doc-test"}}}, now)
	text := strings.Join(lines, "\n")
	if !strings.Contains(text, "INSTALLED license is REVOKED") {
		t.Fatalf("revoked license warning was not exercised: %s", text)
	}
	checkPublicDocReferences(t, paths, text)
}

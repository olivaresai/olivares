// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/release"
)

const manifestAuthAlgorithm = "hmac-sha256-kek-v1"

// CheckImportCompatibility refuses a bundle produced by a newer engine before
// any restore write. Both sides must be orderable release versions; an
// unstamped binary cannot claim that a stamped export is compatible.
func CheckImportCompatibility(m *Manifest, currentVersion string) error {
	if m == nil || strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("DR manifest does not declare the producing engine version")
	}
	producer, err := release.ParseVersion(m.Version)
	if err != nil || release.IsUnstamped(m.Version) {
		return fmt.Errorf("DR manifest engine version %q is not an orderable release version", m.Version)
	}
	if release.IsUnstamped(currentVersion) {
		return fmt.Errorf("this binary is unstamped (%s), so it cannot prove compatibility with export %s; use a released Olivares binary", currentVersion, m.Version)
	}
	current, err := release.ParseVersion(currentVersion)
	if err != nil {
		return fmt.Errorf("running engine version %q is not orderable: %w", currentVersion, err)
	}
	if release.Compare(producer, current) > 0 {
		return fmt.Errorf("refusing DR import from newer engine %s into %s; install %s or later first", m.Version, currentVersion, m.Version)
	}
	return nil
}

// AuthenticateBundle fills the per-file digest inventory and authenticates the complete
// manifest under the operator KEK. Call it after snapshot/key sealing and before
// WriteBundle.
func AuthenticateBundle(in BundleInput, cipher *KeyCipher) error {
	if in.Manifest == nil || cipher == nil || len(cipher.kek) != kekLen {
		return fmt.Errorf("dr: authenticate bundle requires a manifest and initialized KEK")
	}
	files, err := bundleFileDigests(in)
	if err != nil {
		return err
	}
	in.Manifest.Files = files
	in.Manifest.Authentication = ManifestAuthentication{Algorithm: manifestAuthAlgorithm}
	value, err := manifestMAC(in.Manifest, cipher.kek)
	if err != nil {
		return err
	}
	in.Manifest.Authentication.Value = value
	return nil
}

// VerifyBundleIntegrity proves the keyed manifest authentication and every declared
// payload before restore writes anything. Legacy unsigned bundles require an
// explicit caller decision; their historical snapshot digest is still checked
// by the caller, but they do not satisfy the v2 migration contract.
func VerifyBundleIntegrity(work string, m *Manifest, _ KDFParams, cipher *KeyCipher, allowLegacyUnsigned bool) error {
	if m == nil || cipher == nil || len(cipher.kek) != kekLen {
		return fmt.Errorf("dr: verify bundle integrity requires a manifest and initialized KEK")
	}
	if m.Authentication.Algorithm == "" && m.Authentication.Value == "" && len(m.Files) == 0 {
		if allowLegacyUnsigned {
			return nil
		}
		return fmt.Errorf("DR manifest is unsigned and has no per-file digest inventory; pass --allow-legacy-unsigned only for a separately authenticated pre-v26.9 bundle")
	}
	if m.Authentication.Algorithm != manifestAuthAlgorithm {
		return fmt.Errorf("dr: unsupported manifest authentication %q", m.Authentication.Algorithm)
	}
	want, err := manifestMAC(m, cipher.kek)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(want), []byte(m.Authentication.Value)) != 1 {
		return fmt.Errorf("DR manifest authentication mismatch (wrong KEK or altered manifest)")
	}

	expected := map[string]bool{bundleKEKName: true} //verifier-truth:allow expected is the file set the manifest declares; presence is verified below against the bundle, entry by entry
	if m.Store.Method != MethodPITR {
		expected[m.Store.File] = true
	}
	for _, ref := range m.Keys {
		expected[ref.File] = true
	}
	if len(m.Files) != len(expected) {
		return fmt.Errorf("DR manifest file inventory has %d entries, want %d", len(m.Files), len(expected))
	}
	seen := map[string]bool{}
	for _, declared := range m.Files {
		if !expected[declared.Path] || seen[declared.Path] {
			return fmt.Errorf("DR manifest contains unexpected or duplicate file digest for %q", declared.Path)
		}
		seen[declared.Path] = true
		payloadPath, joinErr := safeJoin(work, declared.Path)
		if joinErr != nil {
			return joinErr
		}
		body, err := os.ReadFile(payloadPath)
		if err != nil {
			return fmt.Errorf("read DR payload %s: %w", declared.Path, err)
		}
		sum := sha256.Sum256(body)
		got := hex.EncodeToString(sum[:])
		if int64(len(body)) != declared.SizeBytes || got != declared.SHA256 {
			return fmt.Errorf("DR payload digest mismatch for %s (corrupt or altered export)", declared.Path)
		}
	}
	return nil
}

func bundleFileDigests(in BundleInput) ([]FileDigest, error) {
	expectedKeys := map[string]bool{}
	for _, ref := range in.Manifest.Keys {
		if ref.File == "" || expectedKeys[ref.File] {
			return nil, fmt.Errorf("dr: duplicate or empty key payload path %q", ref.File)
		}
		if _, err := safeJoin(filepath.Clean(string(filepath.Separator)+"bundle-root"), ref.File); err != nil {
			return nil, err
		}
		expectedKeys[ref.File] = true
		if _, ok := in.SealedKeys[ref.File]; !ok {
			return nil, fmt.Errorf("dr: manifest key %s has no sealed payload", ref.File)
		}
	}
	if len(expectedKeys) != len(in.SealedKeys) {
		return nil, fmt.Errorf("dr: sealed key payload set differs from manifest key set")
	}
	kekBytes, err := json.MarshalIndent(in.KEK, "", "  ")
	if err != nil {
		return nil, err
	}
	files := []FileDigest{digestBytes(bundleKEKName, kekBytes)}
	for _, name := range sortedKeys(in.SealedKeys) {
		files = append(files, digestBytes(name, in.SealedKeys[name]))
	}
	if in.Manifest.Store.Method != MethodPITR {
		if in.SnapshotPath == "" {
			return nil, fmt.Errorf("dr: non-PITR bundle has no snapshot payload")
		}
		if _, err := safeJoin(filepath.Clean(string(filepath.Separator)+"bundle-root"), in.Manifest.Store.File); err != nil {
			return nil, err
		}
		sum, size, err := FileSHA256(in.SnapshotPath)
		if err != nil {
			return nil, err
		}
		if sum != in.Manifest.Store.SHA256 || size != in.Manifest.Store.SizeBytes {
			return nil, fmt.Errorf("dr: snapshot bytes differ from manifest before bundle write")
		}
		files = append(files, FileDigest{Path: in.Manifest.Store.File, SizeBytes: size, SHA256: sum})
	} else if in.SnapshotPath != "" {
		return nil, fmt.Errorf("dr: PITR companion must not carry snapshot bytes")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func digestBytes(name string, body []byte) FileDigest {
	sum := sha256.Sum256(body)
	return FileDigest{Path: name, SizeBytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
}

func manifestMAC(m *Manifest, key []byte) (string, error) {
	copyManifest := *m
	copyManifest.Authentication.Value = ""
	body, err := json.Marshal(copyManifest)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("olivares.dr.manifest.auth.v1\x00"))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

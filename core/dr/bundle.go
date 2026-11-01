// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// A DR bundle is a single gzip-compressed tar with a fixed, flat layout:
//
//	manifest.json          the non-secret control record (Manifest)
//	keys/kek.json          the KDF parameters to re-derive the KEK (KDFParams)
//	keys/<name>.enc        each signing key, AES-256-GCM sealed under the KEK
//	store/<snapshot>.enc   the consistent store snapshot, sealed in chunks under
//	                       the KEK (snapshotenc.go); absent for PITR. A bundle
//	                       taken before manifest v2 carries store/<snapshot> in the clear.
//
// The snapshot's SHA-256 in manifest.Store.SHA256 is over the PLAINTEXT snapshot,
// so a restore opens the sealed member, re-digests the result and refuses a
// mismatch. The sealed member has its own digest in manifest.Files. Compression is
// for transport only and never affects either digest.

const (
	bundleManifestName = "manifest.json"
	bundleKEKName      = "keys/kek.json"
)

// BundleInput is what WriteBundle serializes.
type BundleInput struct {
	// Manifest is the control record (written as manifest.json).
	Manifest *Manifest
	// KEK is the KDF parameters to re-derive the key-encryption key (keys/kek.json).
	KEK KDFParams
	// SnapshotPath is the on-disk path of the store snapshot to stream into the
	// bundle at Manifest.Store.File. Empty for MethodPITR (no bytes, external
	// archive); then no store/ entry is written.
	SnapshotPath string
	// SealedKeys maps an in-bundle path ("keys/<name>.enc") to its AES-GCM blob.
	SealedKeys map[string][]byte

	// sealedSnapshot is the sealed twin of SnapshotPath that goes in the tar in its
	// place. Only WriteAuthenticatedBundle sets it, so a caller cannot ship a
	// plaintext snapshot under a manifest that says it is sealed.
	sealedSnapshot string
}

// WriteAuthenticatedBundle seals the store snapshot under the KEK, authenticates
// the complete manifest and payload inventory, then streams the bundle. Every
// current producer must use this API. It marks the manifest as the current format
// and, for a snapshot-carrying bundle, as sealed.
func WriteAuthenticatedBundle(w io.Writer, in BundleInput, cipher *KeyCipher) error {
	if in.Manifest == nil || cipher == nil || len(cipher.kek) != kekLen {
		return fmt.Errorf("dr: write bundle requires a manifest and initialized KEK")
	}
	if in.Manifest.Store.Method != MethodPITR && in.SnapshotPath != "" {
		sealed, err := sealSnapshotFile(cipher, in.SnapshotPath)
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(sealed) }()
		in.sealedSnapshot = sealed
		in.Manifest.Store.Encryption = snapshotEncChunked
	}
	in.Manifest.Format = ManifestFormat
	if err := AuthenticateBundle(in, cipher); err != nil {
		return err
	}
	return WriteBundle(w, in)
}

// WriteBundle is the low-level serializer used after AuthenticateBundle. It
// refuses an unsigned input so a new producer cannot silently emit the legacy
// format. It does not buffer the snapshot in memory.
func WriteBundle(w io.Writer, in BundleInput) error {
	if in.Manifest == nil {
		return fmt.Errorf("dr: WriteBundle nil manifest")
	}
	if in.Manifest.Authentication.Algorithm == "" || in.Manifest.Authentication.Value == "" || len(in.Manifest.Files) == 0 {
		return fmt.Errorf("dr: WriteBundle requires an authenticated manifest and per-file inventory")
	}
	if in.Manifest.Store.sealed() && in.sealedSnapshot == "" && in.SnapshotPath != "" {
		return fmt.Errorf("dr: WriteBundle: the manifest says the snapshot is sealed but only the plaintext was given")
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	mb, err := json.MarshalIndent(in.Manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := writeTarBytes(tw, bundleManifestName, mb); err != nil {
		return err
	}
	kb, err := json.MarshalIndent(in.KEK, "", "  ")
	if err != nil {
		return err
	}
	if err := writeTarBytes(tw, bundleKEKName, kb); err != nil {
		return err
	}
	// Encrypted key files (sorted for a deterministic bundle).
	for _, name := range sortedKeys(in.SealedKeys) {
		if err := writeTarBytes(tw, name, in.SealedKeys[name]); err != nil {
			return err
		}
	}
	// Store snapshot (streamed), if any: the sealed twin when the manifest says sealed.
	if in.SnapshotPath != "" {
		src := in.SnapshotPath
		if in.sealedSnapshot != "" {
			src = in.sealedSnapshot
		}
		if err := writeTarFile(tw, in.Manifest.Store.memberPath(), src); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// ExtractBundle reads a DR bundle from r into destDir, returning the parsed
// manifest and KDF parameters. It needs no KEK, so it does not open the snapshot:
// that member lands as it travels, at destDir/<manifest.Store.File> for a pre-v2
// bundle and at that path plus ".enc" for a sealed one (VerifyBundleIntegrity opens
// it). Each sealed key lands at destDir/<KeyRef.File>.
// Entry names are validated to stay within destDir (no absolute paths, no "..").
func ExtractBundle(r io.Reader, destDir string) (*Manifest, KDFParams, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, KDFParams{}, fmt.Errorf("dr: open bundle: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)

	var (
		m      *Manifest
		kek    KDFParams
		hasKEK bool
		seen   = map[string]bool{}
	)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, KDFParams{}, err
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, KDFParams{}, fmt.Errorf("dr: bundle entry %q is not a regular file", hdr.Name)
		}
		clean, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return nil, KDFParams{}, err
		}
		if seen[hdr.Name] {
			return nil, KDFParams{}, fmt.Errorf("dr: duplicate bundle entry %q", hdr.Name)
		}
		seen[hdr.Name] = true
		switch path.Clean(hdr.Name) {
		case bundleManifestName:
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, KDFParams{}, err
			}
			var parsed Manifest
			if err := json.Unmarshal(b, &parsed); err != nil {
				return nil, KDFParams{}, fmt.Errorf("dr: parse manifest: %w", err)
			}
			if parsed.Format != ManifestFormat && parsed.Format != ManifestFormatV1 {
				return nil, KDFParams{}, fmt.Errorf("dr: unknown manifest format %q (want %q or %q)", parsed.Format, ManifestFormat, ManifestFormatV1)
			}
			m = &parsed
		case bundleKEKName:
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, KDFParams{}, err
			}
			if err := json.Unmarshal(b, &kek); err != nil {
				return nil, KDFParams{}, fmt.Errorf("dr: parse kek params: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(clean), 0o700); err != nil {
				return nil, KDFParams{}, err
			}
			if err := os.WriteFile(clean, b, 0o600); err != nil {
				return nil, KDFParams{}, err
			}
			hasKEK = true
		default:
			if err := extractTo(clean, tr); err != nil {
				return nil, KDFParams{}, err
			}
		}
	}
	if m == nil {
		return nil, KDFParams{}, fmt.Errorf("dr: bundle has no manifest.json")
	}
	if !hasKEK {
		return nil, KDFParams{}, fmt.Errorf("dr: bundle has no keys/kek.json")
	}
	// Legacy manifests have no authenticated inventory. Check every path a caller
	// will read or write before returning it, including when no digest was given.
	if m.Authentication.Algorithm == "" {
		// PITR normally names an external archive, not a local snapshot. A claimed
		// digest still makes the legacy CLI hash this path, so check it too.
		if m.Store.Method != MethodPITR || m.Store.SHA256 != "" {
			if _, err := safeJoin(destDir, m.Store.File); err != nil {
				return nil, KDFParams{}, err
			}
		}
		for _, key := range m.Keys {
			if _, err := safeJoin(destDir, key.File); err != nil {
				return nil, KDFParams{}, err
			}
			if key.Name == "" || key.Name == "." || key.Name == ".." || filepath.Base(key.Name) != key.Name || strings.Contains(key.Name, `\`) {
				return nil, KDFParams{}, fmt.Errorf("dr: unsafe legacy key name %q", key.Name)
			}
		}
	}
	return m, kek, nil
}

func writeTarBytes(tw *tar.Writer, name string, b []byte) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o600, Size: int64(len(b)), Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

func writeTarFile(tw *tar.Writer, name, srcPath string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o600, Size: info.Size(), Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func extractTo(dst string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// safeJoin joins a tar entry name onto root, refusing absolute paths and any
// component that would escape root (path traversal hardening).
func safeJoin(root, name string) (string, error) {
	// TAR paths use '/'. Reject a backslash rather than normalizing it: accepting
	// both spellings would let two distinct headers target one extracted path.
	if name == "" || strings.Contains(name, `\`) || path.IsAbs(name) {
		return "", fmt.Errorf("dr: unsafe bundle entry %q", name)
	}
	clean := path.Clean(name)
	// Refuse rather than rewrite non-canonical names. Rewriting ../escape to
	// escape would keep bytes inside root but would accept precisely the archive
	// shape this boundary promises to reject, and duplicate spellings could then
	// target the same extracted file.
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != name {
		return "", fmt.Errorf("dr: unsafe bundle entry %q", name)
	}
	joined := filepath.Join(root, filepath.FromSlash(clean))
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("dr: bundle entry %q escapes destination", name)
	}
	return joined, nil
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// insertion sort: small key set, avoids importing sort for one call site
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

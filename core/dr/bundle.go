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
//	store/<snapshot>       the consistent store snapshot (absent for PITR)
//
// The snapshot's SHA-256 in manifest.Store.SHA256 is over the RAW snapshot bytes
// (the tar entry payload), so a restore re-digests the extracted file and refuses
// a mismatch. Compression is for transport only and never affects that digest.

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
}

// WriteBundle streams a DR bundle to w. It does not buffer the snapshot in memory.
func WriteBundle(w io.Writer, in BundleInput) error {
	if in.Manifest == nil {
		return fmt.Errorf("dr: WriteBundle nil manifest")
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
	// Store snapshot (streamed), if any.
	if in.SnapshotPath != "" {
		if err := writeTarFile(tw, in.Manifest.Store.File, in.SnapshotPath); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// ExtractBundle reads a DR bundle from r into destDir, returning the parsed
// manifest and KDF parameters. The store snapshot lands at
// destDir/<manifest.Store.File> and each sealed key at destDir/<KeyRef.File>.
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
			continue
		}
		clean, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return nil, KDFParams{}, err
		}
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
			if parsed.Format != ManifestFormat {
				return nil, KDFParams{}, fmt.Errorf("dr: unknown manifest format %q (want %q)", parsed.Format, ManifestFormat)
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
	clean := path.Clean("/" + strings.ReplaceAll(name, `\`, "/"))[1:] // strip leading slash, normalize
	if clean == "" || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("dr: unsafe bundle entry %q", name)
	}
	joined := filepath.Join(root, filepath.FromSlash(clean))
	if joined != root && !strings.HasPrefix(joined, root+string(os.PathSeparator)) {
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

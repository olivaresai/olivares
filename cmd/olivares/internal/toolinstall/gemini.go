// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
)

// Google's official bundle is portable JavaScript, not an npm install script.
// It stays intact beside a fixed launcher. The common release policy verifies
// its exact GitHub SHA-256 before extraction and inventories every installed file.
const geminiLauncher = `#!/bin/sh
PATH=/usr/local/bin:/usr/bin:/bin
export PATH
if ! command -v node >/dev/null 2>&1; then
  echo 'Gemini CLI requires Node.js 20 or newer. Install Node.js and try again.' >&2
  exit 1
fi
node -e 'if (Number(process.versions.node.split(".")[0]) < 20) process.exit(1)' || {
  echo 'Gemini CLI requires Node.js 20 or newer. Upgrade Node.js and try again.' >&2
  exit 1
}
exec node "$(dirname "$0")/../lib/gemini/gemini.js" "$@"
`

func extractGeminiBundle(ctx context.Context, root *os.Root, prefix, fetched string, layout ExpectedLayout) (*ObservedPayloadInventory, error) {
	f, err := root.Open(fetched)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	z, err := zip.NewReader(f, info.Size())
	if err != nil {
		return nil, refuse(KindManifestInvalid, "invalid Gemini bundle: %v", err)
	}
	if len(z.File) > layout.Limits.MaxMembers-4 {
		return nil, refuse(KindResponseTooLarge, "Gemini bundle member count exceeds policy")
	}
	seen := make(map[string]bool)
	var expanded int64
	for _, entry := range z.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name, err := releaseArchivePath(entry.Name)
		if err != nil || name == "" || seen[name] {
			return nil, refuse(KindManifestInvalid, "unsafe or duplicate Gemini bundle member")
		}
		seen[name] = true
		rel := filepath.Join(prefix, "lib", "gemini", filepath.FromSlash(name))
		mode := entry.Mode()
		if mode.IsDir() {
			if entry.UncompressedSize64 != 0 {
				return nil, refuse(KindManifestInvalid, "bundle directory has a payload")
			}
			if err := ensureDirAll(root, rel, 0755); err != nil {
				return nil, err
			}
			continue
		}
		if !mode.IsRegular() {
			return nil, refuse(KindManifestInvalid, "Gemini bundle contains a link or special file")
		}
		if entry.UncompressedSize64 > uint64(layout.Limits.MaxMemberBytes) || entry.UncompressedSize64 > uint64(layout.Limits.MaxExpandedBytes-expanded) {
			return nil, refuse(KindResponseTooLarge, "Gemini bundle expanded size exceeds policy")
		}
		size := int64(entry.UncompressedSize64)
		expanded += size
		if err := ensureDirAll(root, filepath.Dir(rel), 0755); err != nil {
			return nil, err
		}
		src, err := entry.Open()
		if err != nil {
			return nil, err
		}
		dest, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			src.Close()
			return nil, err
		}
		n, copyErr := io.Copy(dest, io.LimitReader(archiveContextReader{ctx: ctx, reader: src}, size+1))
		srcErr := src.Close()
		syncErr := dest.Sync()
		closeErr := dest.Close()
		if copyErr != nil || srcErr != nil || n != size {
			return nil, refuse(KindSizeMismatch, "Gemini bundle member was incomplete or failed its ZIP checksum")
		}
		if syncErr != nil {
			return nil, syncErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	// The official portable bundle has no package manifest. Declare its native
	// ES module format without a version-dependent experimental Node flag.
	if !seen["package.json"] {
		manifest, err := root.OpenFile(filepath.Join(prefix, "lib", "gemini", "package.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		_, writeErr := io.WriteString(manifest, "{\"type\":\"module\"}\n")
		if writeErr == nil {
			writeErr = manifest.Sync()
		}
		closeErr := manifest.Close()
		if writeErr != nil {
			return nil, writeErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	if err := ensureDirAll(root, filepath.Join(prefix, "bin"), 0755); err != nil {
		return nil, err
	}
	launcher, err := root.OpenFile(filepath.Join(prefix, "bin", "gemini"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_, writeErr := io.WriteString(launcher, geminiLauncher)
	if writeErr == nil {
		writeErr = launcher.Sync()
	}
	closeErr := launcher.Close()
	if writeErr != nil {
		return nil, writeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	inv, err := inventoryReleaseArchive(ctx, root, prefix, DriverGemini, layout.Limits)
	if err != nil {
		return nil, err
	}
	return requireArchiveMembers(inv, layout)
}

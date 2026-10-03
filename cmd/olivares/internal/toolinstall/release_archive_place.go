// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package toolinstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/klauspost/compress/zstd"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

type archiveContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r archiveContextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(b)
}

type archiveLink struct{ name, target string }

// Safe archive links are materialized as ordinary files. No extracted symlink
// exists, including while another member is being written. Link copies count
// toward the same expanded-byte limit as ordinary members.
func extractReleaseArchive(ctx context.Context, driver string, root *os.Root, prefix, fetched string, layout ExpectedLayout) (*ObservedPayloadInventory, error) {
	file, err := root.Open(fetched)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	input := archiveContextReader{ctx: ctx, reader: file}
	var reader io.Reader
	if driver == DriverOllama {
		decoder, err := zstd.NewReader(input, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20), zstd.WithDecoderMaxWindow(64<<20))
		if err != nil {
			return nil, refuse(KindManifestInvalid, "invalid zstd archive: %v", err)
		}
		defer decoder.Close()
		reader = decoder
	} else {
		decoder, err := gzip.NewReader(input)
		if err != nil {
			return nil, refuse(KindManifestInvalid, "invalid gzip archive: %v", err)
		}
		defer decoder.Close()
		reader = decoder
	}
	// The decoded-byte bound includes tar headers and PAX metadata, not only
	// placed file payload. Keep the sentinel byte to detect limit exhaustion.
	decoded := &io.LimitedReader{R: reader, N: layout.Limits.MaxExpandedBytes + 1}
	tr := tar.NewReader(archiveContextReader{ctx: ctx, reader: decoded})
	seen := map[string]bool{}
	var links []archiveLink
	var expanded int64
	members := 0
	rootSeen := false
	for {
		hdr, err := tr.Next()
		if decoded.N == 0 {
			return nil, refuse(KindResponseTooLarge, "decoded archive exceeds policy")
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, refuse(KindManifestInvalid, "read archive: %v", err)
		}
		members++
		if members > layout.Limits.MaxMembers {
			return nil, refuse(KindResponseTooLarge, "archive member count exceeds policy")
		}
		name, err := releaseArchivePath(hdr.Name)
		if err != nil {
			return nil, refuse(KindManifestInvalid, "%v", err)
		}
		if name == "" {
			if hdr.Typeflag != tar.TypeDir || rootSeen {
				return nil, refuse(KindManifestInvalid, "invalid root archive member")
			}
			rootSeen = true
			continue
		}
		if driver == DriverOpenCode && name == "opencode" {
			name = "bin/opencode"
		}
		// The Codex archive holds one binary named after its platform target.
		if driver == DriverCodex && strings.HasPrefix(name, "codex-") && strings.HasSuffix(name, "-unknown-linux-musl") && !strings.Contains(name, "/") {
			name = "bin/codex"
		}
		if !releaseArchiveMember(driver, name) || seen[name] {
			return nil, refuse(KindManifestInvalid, "unexpected or duplicate archive member %q", name)
		}
		seen[name] = true
		relative := filepath.Join(prefix, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if name == layout.EntryPoint {
				return nil, refuse(KindManifestInvalid, "entry point is a directory")
			}
			if err := ensureDirAll(root, relative, 0755); err != nil {
				return nil, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if name == "bin" || name == "lib" || name == "lib/ollama" {
				return nil, refuse(KindManifestInvalid, "archive directory is a file")
			}
			if hdr.Size < 0 || hdr.Size > layout.Limits.MaxMemberBytes || expanded > layout.Limits.MaxExpandedBytes-hdr.Size {
				return nil, refuse(KindResponseTooLarge, "archive member or expanded size exceeds policy")
			}
			expanded += hdr.Size
			if err := ensureDirAll(root, filepath.Dir(relative), 0755); err != nil {
				return nil, err
			}
			target, err := root.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return nil, err
			}
			n, copyErr := io.Copy(target, io.LimitReader(tr, hdr.Size+1))
			syncErr := target.Sync()
			closeErr := target.Close()
			if copyErr != nil || n != hdr.Size {
				return nil, refuse(KindSizeMismatch, "archive member %q was incomplete", name)
			}
			if syncErr != nil {
				return nil, syncErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
		case tar.TypeSymlink, tar.TypeLink:
			if driver != DriverOllama || !strings.HasPrefix(name, "lib/ollama/") {
				return nil, refuse(KindManifestInvalid, "links are only accepted within the Ollama library tree")
			}
			raw := hdr.Linkname
			if path.IsAbs(raw) || strings.ContainsAny(raw, "\\\x00\r\n") || raw == "" {
				return nil, refuse(KindManifestInvalid, "unsafe archive link target")
			}
			target := raw
			if hdr.Typeflag == tar.TypeSymlink {
				target = path.Join(path.Dir(name), raw)
			}
			target, err = releaseArchivePath(target)
			if err != nil || !strings.HasPrefix(target, "lib/ollama/") {
				return nil, refuse(KindManifestInvalid, "archive link escapes the library tree")
			}
			links = append(links, archiveLink{name: name, target: target})
		default:
			return nil, refuse(KindManifestInvalid, "archive member %q has an unsupported type", name)
		}
	}
	for len(links) > 0 {
		remaining := make([]archiveLink, 0, len(links))
		copied := 0
		for _, link := range links {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			src := filepath.Join(prefix, filepath.FromSlash(link.target))
			fi, err := root.Lstat(src)
			if os.IsNotExist(err) {
				remaining = append(remaining, link)
				continue
			}
			if err != nil || !fi.Mode().IsRegular() {
				return nil, refuse(KindManifestInvalid, "archive link does not target a regular file")
			}
			if fi.Size() > layout.Limits.MaxMemberBytes || expanded > layout.Limits.MaxExpandedBytes-fi.Size() {
				return nil, refuse(KindResponseTooLarge, "materialized archive links exceed expanded size policy")
			}
			expanded += fi.Size()
			dest := filepath.Join(prefix, filepath.FromSlash(link.name))
			if err := ensureDirAll(root, filepath.Dir(dest), 0755); err != nil {
				return nil, err
			}
			if err := copyPlacedFile(root, src, dest, 0600); err != nil {
				return nil, err
			}
			copied++
		}
		if copied == 0 {
			return nil, refuse(KindManifestInvalid, "archive links are dangling or cyclic")
		}
		links = remaining
	}
	inventory, err := inventoryReleaseArchive(ctx, root, prefix, driver, layout.Limits)
	if err != nil {
		return nil, err
	}
	for _, required := range layout.Members {
		found := false
		for _, member := range inventory.Members {
			if member.Path == required.Path && member.Kind == required.Kind {
				found = true
				break
			}
		}
		if !found {
			return nil, refuse(KindManifestInvalid, "required archive member %q is missing", required.Path)
		}
	}
	return inventory, nil
}

func inventoryReleaseArchive(ctx context.Context, root *os.Root, prefix, driver string, limits ExtractionLimits) (*ObservedPayloadInventory, error) {
	inv := &ObservedPayloadInventory{Members: []ObservedMember{}}
	var expanded int64
	err := fs.WalkDir(root.FS(), prefix, func(relative string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if relative == prefix {
			return nil
		}
		name := filepath.ToSlash(strings.TrimPrefix(relative, prefix+string(filepath.Separator)))
		if name == fetchedObjectName || name == stagingMarker || name == ReceiptFile {
			return nil
		}
		if !releaseArchiveMember(driver, name) {
			return refuse(KindDamaged, "unexpected installed archive member %q", name)
		}
		fi, err := root.Lstat(relative)
		if err != nil {
			return err
		}
		member := ObservedMember{Path: name, Mode: uint32(fi.Mode().Perm())}
		switch {
		case fi.IsDir():
			member.Kind = MemberKindDirectory
		case fi.Mode().IsRegular():
			member.Kind = MemberKindRegular
			if fi.Size() > limits.MaxMemberBytes || expanded > limits.MaxExpandedBytes-fi.Size() {
				return refuse(KindResponseTooLarge, "installed archive payload exceeds policy")
			}
			expanded += fi.Size()
			sum, size, err := fileSHA256Root(root, relative)
			if err != nil {
				return err
			}
			member.SHA256 = sum
			member.Size = size
		default:
			return refuse(KindDamaged, "installed archive member %q is a link or special file", name)
		}
		inv.Members = append(inv.Members, member)
		if len(inv.Members) > limits.MaxMembers {
			return refuse(KindResponseTooLarge, "installed archive member count exceeds policy")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(inv.Members, func(i, j int) bool { return inv.Members[i].Path < inv.Members[j].Path })
	return inv, nil
}

func verifyReleaseArchiveInventory(ctx context.Context, access PayloadAccess, inv *ObservedPayloadInventory, driver string, limits ExtractionLimits) error {
	if inv == nil || len(inv.Members) == 0 || len(inv.Members) > limits.MaxMembers {
		return refuse(KindDamaged, "archive inventory is missing or exceeds policy")
	}
	previous := ""
	var expanded int64
	for _, member := range inv.Members {
		if err := ctx.Err(); err != nil {
			return err
		}
		if member.Path <= previous || !releaseArchiveMember(driver, member.Path) {
			return refuse(KindDamaged, "archive inventory paths are not unique, confined and canonical")
		}
		previous = member.Path
		fi, err := access.Lstat(member.Path)
		if err != nil {
			return err
		}
		if member.Kind == MemberKindDirectory {
			if !fi.IsDir() {
				return refuse(KindDamaged, "archive directory changed")
			}
			continue
		}
		if member.Kind != MemberKindRegular || !fi.Mode().IsRegular() || fi.Size() != member.Size || member.Size < 0 || member.Size > limits.MaxMemberBytes || expanded > limits.MaxExpandedBytes-member.Size {
			return refuse(KindDamaged, "archive file size or kind changed")
		}
		expanded += member.Size
		file, err := access.OpenFile(member.Path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, readErr := io.Copy(hash, io.LimitReader(archiveContextReader{ctx: ctx, reader: file}, member.Size+1))
		file.Close()
		if readErr != nil || n != member.Size || hex.EncodeToString(hash.Sum(nil)) != member.SHA256 {
			return refuse(KindDamaged, "archive file %q does not match its receipt digest", member.Path)
		}
	}
	return nil
}
func applyReleaseArchiveModes(root *os.Root, prefix string, inv *ObservedPayloadInventory) error {
	for i := range inv.Members {
		m := &inv.Members[i]
		mode := os.FileMode(0644)
		if m.Kind == MemberKindDirectory || strings.HasPrefix(m.Path, "bin/") || strings.HasPrefix(path.Base(m.Path), "llama-server") || strings.HasPrefix(path.Base(m.Path), "llama-quantize") {
			mode = 0755
		}
		if err := root.Chmod(filepath.Join(prefix, filepath.FromSlash(m.Path)), mode); err != nil {
			return err
		}
		m.Mode = uint32(mode)
	}
	return nil
}
func verifyReleaseArchiveReceipt(ctx context.Context, root *os.Root, prefix string, rec *ReceiptV2) error {
	if releaseRepository(rec.Driver) == "" || rec.PackagePolicyID != PackagePolicyReleaseArchiveV1 || rec.VerificationKind != VerificationGitHubReleaseSHA256 {
		return refuse(KindDamaged, "release archive receipt has a different verification policy")
	}
	inv, err := inventoryReleaseArchive(ctx, root, prefix, rec.Driver, releaseArchiveLayout(rec.Driver).Limits)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(inv.Members, rec.Payload.Members) {
		return refuse(KindDamaged, "release archive files do not match the receipt inventory")
	}
	return nil
}

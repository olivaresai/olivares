// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func copyPlacedFile(root *os.Root, srcRel, dstRel string, perm os.FileMode) error {
	src, err := root.Open(srcRel)
	if err != nil {
		return refuse(KindDestinationNotWritable, "open fetched object %s: %v", srcRel, err)
	}
	defer func() { _ = src.Close() }()
	dst, err := root.OpenFile(dstRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return refuse(KindDestinationNotWritable, "create %s: %v", dstRel, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return refuse(KindDestinationNotWritable, "write %s: %v", dstRel, err)
	}
	if err := dst.Sync(); err != nil {
		_ = dst.Close()
		return refuse(KindDestinationNotWritable, "fsync %s: %v", dstRel, err)
	}
	if err := dst.Close(); err != nil {
		return refuse(KindDestinationNotWritable, "close %s: %v", dstRel, err)
	}
	return nil
}

func inventoryLayout(root *os.Root, prefix string, layout ExpectedLayout) (*ObservedPayloadInventory, error) {
	inv := &ObservedPayloadInventory{Members: make([]ObservedMember, 0, len(layout.Members))}
	for _, m := range layout.Members {
		rel := filepath.Join(prefix, filepath.FromSlash(m.Path))
		fi, err := root.Lstat(rel)
		if err != nil {
			return nil, refuse(KindDamaged, "placed member %s is missing: %v", m.Path, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, refuse(KindDamaged, "placed member %s is a symlink", m.Path)
		}
		om := ObservedMember{Path: m.Path, Mode: uint32(fi.Mode().Perm())}
		switch m.Kind {
		case MemberKindDirectory:
			if !fi.IsDir() {
				return nil, refuse(KindDamaged, "placed member %s is not a directory", m.Path)
			}
			om.Kind = MemberKindDirectory
		case MemberKindRegular:
			if !fi.Mode().IsRegular() {
				return nil, refuse(KindDamaged, "placed member %s is not a regular file", m.Path)
			}
			om.Kind = MemberKindRegular
			sum, size, err := fileSHA256Root(root, rel)
			if err != nil {
				return nil, refuse(KindDamaged, "hash %s: %v", m.Path, err)
			}
			om.SHA256, om.Size = sum, size
		default:
			return nil, refuse(KindDamaged, "placed member %s has unknown kind %s", m.Path, m.Kind)
		}
		inv.Members = append(inv.Members, om)
	}
	return inv, nil
}

func extractTarGz(ctx context.Context, root *os.Root, destRel, fetchedRel string, layout ExpectedLayout) error {
	src, err := root.Open(fetchedRel)
	if err != nil {
		return refuse(KindDestinationNotWritable, "open fetched package %s: %v", fetchedRel, err)
	}
	defer func() { _ = src.Close() }()
	gz, err := gzip.NewReader(src)
	if err != nil {
		return refuse(KindManifestInvalid, "fetched object is not a gzip archive: %v", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	allowed := map[string]ExpectedMember{}
	for _, m := range layout.Members {
		allowed[m.Path] = m
	}
	seen := map[string]struct{}{}
	var expanded int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return refuse(KindManifestInvalid, "read package tar: %v", err)
		}
		name := strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./")
		if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return refuse(KindManifestInvalid, "package member %q is not a relative slash-separated path", hdr.Name)
		}
		want, ok := allowed[name]
		if !ok {
			return refuse(KindManifestInvalid, "package member %q is not in the closed layout", name)
		}
		if _, dup := seen[name]; dup {
			return refuse(KindManifestInvalid, "package member %q appears more than once", name)
		}
		seen[name] = struct{}{}
		rel := filepath.Join(destRel, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if want.Kind != MemberKindDirectory {
				return refuse(KindManifestInvalid, "package member %q is a directory, layout wants %s", name, want.Kind)
			}
			if err := ensureDirAll(root, rel, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if want.Kind != MemberKindRegular {
				return refuse(KindManifestInvalid, "package member %q is a file, layout wants %s", name, want.Kind)
			}
			if hdr.Size < 0 || hdr.Size > layout.Limits.MaxMemberBytes {
				return refuse(KindResponseTooLarge, "package member %q size %d exceeds max_member_bytes %d", name, hdr.Size, layout.Limits.MaxMemberBytes)
			}
			expanded += hdr.Size
			if expanded > layout.Limits.MaxExpandedBytes {
				return refuse(KindResponseTooLarge, "package expanded size exceeds max_expanded_bytes %d", layout.Limits.MaxExpandedBytes)
			}
			if err := ensureDirAll(root, filepath.Dir(rel), 0o755); err != nil {
				return err
			}
			dst, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return refuse(KindDestinationNotWritable, "create %s: %v", rel, err)
			}
			n, copyErr := io.Copy(dst, io.LimitReader(tr, hdr.Size+1))
			closeErr := dst.Close()
			if copyErr != nil {
				return refuse(KindDestinationNotWritable, "write %s: %v", rel, copyErr)
			}
			if n != hdr.Size {
				return refuse(KindSizeMismatch, "package member %q delivered %d bytes, tar header %d", name, n, hdr.Size)
			}
			if closeErr != nil {
				return refuse(KindDestinationNotWritable, "close %s: %v", rel, closeErr)
			}
		default:
			return refuse(KindManifestInvalid, "package member %q has tar type %v; only regular files and directories are accepted", name, hdr.Typeflag)
		}
	}
	for _, m := range layout.Members {
		if _, ok := seen[m.Path]; ok {
			continue
		}
		if m.Kind == MemberKindDirectory {
			rel := filepath.Join(destRel, filepath.FromSlash(m.Path))
			if err := ensureDirAll(root, rel, 0o755); err != nil {
				return err
			}
			continue
		}
		return refuse(KindManifestInvalid, "closed layout member %q is missing from the package", m.Path)
	}
	return nil
}

func applyFinalModes(root *os.Root, prefix string, layout ExpectedLayout) error {
	for _, m := range layout.Members {
		rel := filepath.Join(prefix, filepath.FromSlash(m.Path))
		if err := root.Chmod(rel, fs.FileMode(m.FinalMode)); err != nil {
			return refuse(KindDestinationNotWritable, "chmod %s: %v", m.Path, err)
		}
	}
	return nil
}

func memberHash(inv *ObservedPayloadInventory, path string) (string, int64, error) {
	if inv == nil {
		return "", 0, fmt.Errorf("inventory is missing")
	}
	for _, m := range inv.Members {
		if m.Path == path {
			return m.SHA256, m.Size, nil
		}
	}
	return "", 0, fmt.Errorf("inventory has no member %s", path)
}

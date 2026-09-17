// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package toolinstall

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// probeSafety decides whether a detected file may be executed by a probe: the
// file and its containing directory must be owned by this process's uid or by
// root, and neither may be group- or world-writable. Root-owned read-only system
// tools therefore stay usable to an ordinary caller; a file another account
// could have replaced is refused by name.
func probeSafety(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("containing directory: %w", err)
	}
	return checkProbeSafety(fi, dir, filepath.Dir(path), os.Geteuid())
}

// checkProbeSafety is the pure decision over the two stat results.
func checkProbeSafety(file, dir os.FileInfo, dirPath string, euid int) error {
	if file.Mode()&os.ModeSymlink != 0 || !file.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", file.Name())
	}
	if file.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", file.Name())
	}
	if file.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is group- or world-writable (%s); another account could have replaced its bytes", file.Name(), file.Mode().Perm())
	}
	uid, ok := ownerUID(file)
	if !ok {
		return fmt.Errorf("cannot read the owner of %s", file.Name())
	}
	if !trustedOwner(uid, euid) {
		return fmt.Errorf("%s is owned by uid %d; only files owned by this process (uid %d) or by root are probed", file.Name(), uid, euid)
	}
	if !dir.IsDir() {
		return fmt.Errorf("%s is not a directory", dirPath)
	}
	if dir.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("containing directory %s is group- or world-writable (%s); another account could replace the file", dirPath, dir.Mode().Perm())
	}
	duid, ok := ownerUID(dir)
	if !ok {
		return fmt.Errorf("cannot read the owner of %s", dirPath)
	}
	if !trustedOwner(duid, euid) {
		return fmt.Errorf("containing directory %s is owned by uid %d; only directories owned by this process (uid %d) or by root are trusted", dirPath, duid, euid)
	}
	return nil
}

// trustedOwner accepts the caller's own uid and root. There is no other
// exception: a file belonging to any other account is not probed.
func trustedOwner(uid uint32, euid int) bool {
	return uid == 0 || int(uid) == euid
}

func ownerUID(fi os.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

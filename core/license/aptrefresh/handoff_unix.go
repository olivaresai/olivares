// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package aptrefresh

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// getuid is the uid the handoff directory must belong to. Only tests replace it.
var getuid = os.Getuid

// publish writes data as <dir>/<cycle>.json (Interface Q3 r3 §3.1.5.5). The directory is opened with
// O_DIRECTORY|O_NOFOLLOW and must be, by fstat on that descriptor, a directory of the running uid with
// mode 0700. Relative to that descriptor, .<cycle>.tmp is created with O_CREAT|O_EXCL|O_NOFOLLOW and mode
// 0600, written, fsynced and renamed over <cycle>.json, and the directory is fsynced. A .<cycle>.tmp that
// already exists, file or symlink, refuses and is left as it was. No path outside dir is opened.
func publish(dir, cycle string, data []byte) error {
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("%w: open %s: %v", ErrHandoffCustody, dir, err)
	}
	defer func() { _ = unix.Close(dfd) }()
	var st unix.Stat_t
	if err := unix.Fstat(dfd, &st); err != nil {
		return fmt.Errorf("%w: fstat %s: %v", ErrHandoffCustody, dir, err)
	}
	if uid := getuid(); st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != uid || st.Mode&0o7777 != 0o700 {
		return fmt.Errorf("%w: %s must be a directory of uid %d with mode 0700 (found uid %d, mode %04o)",
			ErrHandoffCustody, dir, uid, st.Uid, st.Mode&0o7777)
	}
	tmp, final := "."+cycle+".tmp", cycle+".json"
	fd, err := unix.Openat(dfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("%w: %s/%s already exists", ErrHandoffCustody, dir, tmp)
	}
	if err != nil {
		return fmt.Errorf("create %s/%s: %w", dir, tmp, err)
	}
	f := os.NewFile(uintptr(fd), tmp)
	discard := func(cause error) error {
		_ = f.Close()
		if uerr := unix.Unlinkat(dfd, tmp, 0); uerr != nil {
			return errors.Join(cause, fmt.Errorf("remove %s/%s: %w", dir, tmp, uerr))
		}
		return cause
	}
	// The umask can narrow the created mode; the helper requires exactly 0600.
	if err := f.Chmod(0o600); err != nil {
		return discard(fmt.Errorf("chmod %s/%s: %w", dir, tmp, err))
	}
	if _, err := f.Write(data); err != nil {
		return discard(fmt.Errorf("write %s/%s: %w", dir, tmp, err))
	}
	if err := f.Sync(); err != nil {
		return discard(fmt.Errorf("sync %s/%s: %w", dir, tmp, err))
	}
	if err := f.Close(); err != nil {
		_ = unix.Unlinkat(dfd, tmp, 0)
		return fmt.Errorf("close %s/%s: %w", dir, tmp, err)
	}
	if err := unix.Renameat(dfd, tmp, dfd, final); err != nil {
		_ = unix.Unlinkat(dfd, tmp, 0)
		return fmt.Errorf("publish %s/%s: %w", dir, final, err)
	}
	if err := unix.Fsync(dfd); err != nil {
		return fmt.Errorf("sync %s after publishing %s: %w", dir, final, err)
	}
	return nil
}

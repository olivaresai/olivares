// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localinstall

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// maxPlacedManifest bounds what Place copies; a v2 manifest is a few hundred bytes.
const maxPlacedManifest = 1 << 20

// Placed is what Place put in place: the manifest's inode and modification time.
type Placed struct {
	Inode   uint64
	ModTime time.Time
}

// String prints the placement as the package script records it: stat -c %i, then stat -c %y with its
// blanks as underscores.
func (p Placed) String() string {
	return fmt.Sprintf("%d %s", p.Inode, strings.ReplaceAll(p.ModTime.Local().Format("2006-01-02 15:04:05.000000000 -0700"), " ", "_"))
}

// Place copies the staged manifest to target from inside target's own directory: a temporary file created there
// (never through a link, never an existing name), owned root:gid and mode 0640, synced, then renamed onto target in
// the same directory, which is synced too. The package script uses it when its staging directory and target's
// directory are on different filesystems, where its own rename cannot be atomic: the data directory
// belongs to the service account, so root writes in it only by descriptor.
func Place(stage, target string, gid int) (Placed, error) {
	if !filepath.IsAbs(stage) || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return Placed{}, errors.New("the stage and the target must be clean absolute paths")
	}
	data, err := readStage(stage)
	if err != nil {
		return Placed{}, err
	}
	// filepath.Dir, not Split: a trailing slash would make the kernel follow a link at the directory itself.
	dir, name := filepath.Dir(target), filepath.Base(target)
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Placed{}, fmt.Errorf("open the target directory %s: %w", dir, err)
	}
	defer unix.Close(dfd)
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Placed{}, err
	}
	temporary := "." + name + ".place-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat2(dfd, temporary, &unix.OpenHow{
		Flags:   unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_NOFOLLOW | unix.O_CLOEXEC,
		Mode:    0o600,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return Placed{}, fmt.Errorf("create a temporary file in %s: %w", dir, err)
	}
	placed, err := fill(fd, data, gid)
	unix.Close(fd)
	if err == nil {
		err = unix.Renameat(dfd, temporary, dfd, name)
	}
	if err != nil {
		_ = unix.Unlinkat(dfd, temporary, 0)
		return Placed{}, fmt.Errorf("place %s: %w", target, err)
	}
	if err := unix.Fsync(dfd); err != nil {
		return Placed{}, fmt.Errorf("sync %s: %w", dir, err)
	}
	return placed, nil
}

func readStage(stage string) ([]byte, error) {
	fd, err := unix.Open(stage, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open the stage %s: %w", stage, err)
	}
	f := os.NewFile(uintptr(fd), stage)
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPlacedManifest+1))
	if err != nil {
		return nil, fmt.Errorf("read the stage %s: %w", stage, err)
	}
	if len(data) > maxPlacedManifest {
		return nil, fmt.Errorf("the stage %s is larger than a manifest", stage)
	}
	return data, nil
}

// fill writes data to the new file, gives it root:gid and 0640, syncs it and returns what it holds.
func fill(fd int, data []byte, gid int) (Placed, error) {
	for written := 0; written < len(data); {
		n, err := unix.Write(fd, data[written:])
		if err != nil {
			return Placed{}, err
		}
		written += n
	}
	if err := unix.Fchown(fd, os.Geteuid(), gid); err != nil {
		return Placed{}, err
	}
	if err := unix.Fchmod(fd, 0o640); err != nil {
		return Placed{}, err
	}
	if err := unix.Fsync(fd); err != nil {
		return Placed{}, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return Placed{}, err
	}
	return Placed{Inode: st.Ino, ModTime: time.Unix(st.Mtim.Unix())}, nil
}

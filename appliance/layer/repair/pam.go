// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package repair

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/olivaresai/olivares/appliance/layer/portal/auth"
)

// PristinePortalPAM is where the portal package keeps its pristine copy of the portal's stack.
const PristinePortalPAM = "/usr/share/olivares-portal/pam.d/" + auth.PAMService

// maxStackBytes bounds a stack file.
const maxStackBytes = 64 * 1024

// PortalPAM restores the portal's sign-in stack, Dir/Name, from the package's pristine copy.
type PortalPAM struct {
	// Pristine is the package's pristine copy, PristinePortalPAM on an installed appliance.
	Pristine string
	// Dir and Name are the stack's directory and service name: /etc/pam.d and olivares-portal.
	Dir, Name string
	// GroupExists answers whether the host has the named group.
	GroupExists func(name string) error
}

// PAMResult is what a restore did and measured.
type PAMResult struct {
	// Restored reports that the pristine copy replaced the stack.
	Restored bool
	// Usable reports that the stack measures usable the way the mode selector measures it.
	Usable bool
	// Reason is a fixed sentence naming the failed check, with no path.
	Reason string
}

// Restore replaces the stack with the pristine copy and measures it. The copy must be a regular
// file, never a link, writable by its owner alone; the stack is written under a fixed temporary
// name, created exclusively, synced, given mode 0644 and renamed over the stack, and the directory
// is synced. Nothing else in the directory changes. A copy that cannot be read restores nothing.
func (p PortalPAM) Restore() (PAMResult, error) {
	if p.Name == "" || filepath.Base(p.Name) != p.Name || p.Name == "." || p.Name == ".." {
		return PAMResult{Reason: "the stack's name is not a file name"}, errors.New("not a file name")
	}
	pristine, err := readProtected(p.Pristine)
	if err != nil {
		return PAMResult{Reason: "the package's pristine copy is absent or not a protected regular file"}, err
	}
	if err := writeProtected(p.Dir, p.Name, pristine, 0o644); err != nil {
		return PAMResult{Reason: "the pristine copy could not be written over the stack"}, err
	}
	return p.measure(pristine), nil
}

// measure reads the stack back as the mode selector measures it: the service file present and
// readable, here byte for byte the package's own, and the olivares-admins group present on the
// host, since a stack whose group does not exist admits nobody.
func (p PortalPAM) measure(pristine []byte) PAMResult {
	data, err := readProtected(filepath.Join(p.Dir, p.Name))
	switch {
	case err != nil:
		return PAMResult{Restored: true, Reason: "the restored stack cannot be read back."}
	case !bytes.Equal(data, pristine):
		return PAMResult{Restored: true, Reason: "the stack read back is not the pristine copy."}
	case p.GroupExists == nil || p.GroupExists(auth.AdministratorsGroup) != nil:
		return PAMResult{Restored: true, Reason: "the " + auth.AdministratorsGroup + " group does not exist on this host, so the stack admits nobody."}
	}
	return PAMResult{Restored: true, Usable: true}
}

// readProtected reads path: a regular file, never a link, writable by its owner alone, of at most
// 64 KiB, and the same file when opened as when measured.
func readProtected(path string) ([]byte, error) {
	measured, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !measured.Mode().IsRegular() || measured.Mode().Perm()&0o022 != 0 || measured.Size() > maxStackBytes {
		return nil, errors.New("not a protected regular file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(measured, opened) {
		return nil, errors.New("the file changed while it was read")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxStackBytes+1))
	if err != nil || len(data) > maxStackBytes {
		return nil, errors.New("unreadable")
	}
	return data, nil
}

// writeProtected replaces name in dir with data: the directory must be a directory, not a link and
// not writable by its group or others; the file is written under a fixed temporary name, created
// exclusively with no link followed, synced, given mode, renamed over name, and the directory is
// synced. A temporary file left by an interrupted run is removed first.
func writeProtected(dir, name string, data []byte, mode os.FileMode) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return errors.New("the directory is not protected")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	temporary := "." + name + ".olivares-repair"
	if err := root.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	chmodErr := f.Chmod(mode)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, chmodErr, syncErr, closeErr); err != nil {
		_ = root.Remove(temporary)
		return err
	}
	if err := root.Rename(temporary, name); err != nil {
		_ = root.Remove(temporary)
		return err
	}
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

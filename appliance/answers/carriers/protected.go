// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package carriers reads one appliance-answers/v1 document from the places an installation
// can deliver it: the file cloud-init writes from a NoCloud seed, the OVF environment VMware
// exposes as guestinfo, a systemd credential and a local file. A carrier emits the document it
// received, unchanged. When several carriers are present they must agree, or the caller gets
// a refusal that names them and nothing they carried.
package carriers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
)

// MaxDocumentBytes is the answers module's input bound; a larger input is refused, not cut.
const MaxDocumentBytes = 64 * 1024

// Runner runs a program with fixed arguments and returns its standard output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ErrUnlistedProgram reports a program that is not on ExecRunner's closed list. It never runs.
var ErrUnlistedProgram = errors.New("first boot runs no such program")

// programs is ExecRunner's closed list, by the exact name each caller passes: the guestinfo
// carrier asks vmware-rpctool, then vmtoolsd (sources.go); the base layer's host adapters run
// cloud-init, systemctl and id from PATH and the product's configuration generator by its
// absolute path (layer/base/adapters.go, on the Host rooted at "/" that appliance-firstboot runs).
var programs = [...]string{"vmware-rpctool", "vmtoolsd", "cloud-init", "systemctl", "id", "/usr/bin/olivares"}

// ExecRunner runs name when it is on the closed list, a tool from PATH or the product binary by
// its absolute path, and refuses any other name before exec. Standard error is discarded: tools
// may echo values there.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	if !slices.Contains(programs[:], name) {
		return nil, fmt.Errorf("%w: %q; it runs only %s", ErrUnlistedProgram, name, strings.Join(programs[:], ", "))
	}
	// #nosec G204 -- name is one of the six literals of programs, checked above; every caller passes constant arguments, and exec hands them to the program as an argument vector, never to a shell.
	return exec.CommandContext(ctx, name, args...).Output()
}

// InputError reports a carrier input that cannot be read safely, by reference and reason.
type InputError struct{ Ref, Reason string }

func (e *InputError) Error() string { return e.Ref + ": " + e.Reason }

// ReadProtected reads a regular file that is not a symbolic link, is owned by the reading
// account and is not writable by group or others. A missing file reports false. A file
// larger than limit is refused rather than truncated.
func ReadProtected(path string, limit int64) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, &InputError{Ref: path, Reason: "cannot be inspected"}
	}
	if reason := unprotected(info); reason != "" {
		return nil, false, &InputError{Ref: path, Reason: reason}
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, false, &InputError{Ref: path, Reason: "cannot be opened without following links"}
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, false, &InputError{Ref: path, Reason: "changed while it was opened"}
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, false, &InputError{Ref: path, Reason: "cannot be read"}
	}
	if int64(len(data)) > limit {
		return nil, false, &InputError{Ref: path, Reason: fmt.Sprintf("is larger than %d bytes", limit)}
	}
	return data, true, nil
}

func unprotected(info os.FileInfo) string {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return "is a symbolic link"
	case !info.Mode().IsRegular():
		return "is not a regular file"
	case info.Mode().Perm()&0o022 != 0:
		return "is writable by group or others"
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return "is not owned by the reading account"
	}
	return ""
}

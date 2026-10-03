// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// systemReadOnly are the directories any program needs to load and run. A
// protected path inside one of them is carved out like any other grant.
var systemReadOnly = []string{
	"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32",
	"/etc", "/opt", "/proc", "/sys", "/snap", "/nix",
}

// deviceFiles are read and written by ordinary programs (and shells).
var deviceFiles = []string{"/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom", "/dev/tty"}

// deviceDirs hold pseudo-terminals and shared memory.
var deviceDirs = []string{"/dev/pts", "/dev/shm"}

// defaultWritable are the paths the helper grants writable to every child.
func defaultWritable() []string {
	return append(append([]string{}, deviceDirs...), deviceFiles...)
}

func landlockABI() (int, error) {
	r, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0, errno
	}
	return int(r), nil
}

func probe() State {
	abi, err := landlockABI()
	switch {
	case errors.Is(err, unix.ENOSYS):
		return State{Mode: ModeNone, Reason: "this Linux kernel has no Landlock (needs 5.13 or later)"}
	case errors.Is(err, unix.EOPNOTSUPP):
		return State{Mode: ModeNone, Reason: "Landlock is disabled at boot on this kernel (add landlock to the lsm= list)"}
	case err != nil:
		return State{Mode: ModeNone, Reason: "Landlock is not usable here: " + err.Error()}
	case abi < 1:
		return State{Mode: ModeNone, Reason: "Landlock reported no usable ABI"}
	}
	return State{Mode: ModeLandlock, ABI: abi}
}

// access are the rights for one ABI: every right on directories the child
// writes, file-only rights for single files, and read+execute for the rest.
type access struct {
	handled, dirRW, fileRW, dirRO, fileRO uint64
}

func accessFor(abi int) access {
	fileRW := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_READ_FILE)
	handled := fileRW | unix.LANDLOCK_ACCESS_FS_READ_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE | unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK | unix.LANDLOCK_ACCESS_FS_MAKE_SYM
	if abi >= 2 {
		handled |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		handled |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
		fileRW |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		handled |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
		fileRW |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	fileRO := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE)
	return access{
		handled: handled,
		dirRW:   handled &^ unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK &^ unix.LANDLOCK_ACCESS_FS_MAKE_CHAR,
		fileRW:  fileRW,
		dirRO:   fileRO | unix.LANDLOCK_ACCESS_FS_READ_DIR,
		fileRO:  fileRO,
	}
}

// ruleset collects path rules for one Landlock domain.
type ruleset struct {
	fd      int
	access  access
	protect []string
}

// grant allows path (directory or file) with the rights for its kind. A path
// that contains a protected path is not granted as a whole: its entries are,
// recursively, except the protected ones. A protected path itself is never
// granted; a path under one is granted only when the policy names it (a tool
// install, an account home). A missing path is skipped.
func (r *ruleset) grant(path string, write bool) error {
	return r.grantPath(realPath(path), write, true)
}

func (r *ruleset) grantPath(real string, write, named bool) error {
	for _, p := range r.protect {
		// An entry found while carving (not named by the policy) that resolves
		// into a protected path, e.g. a symlink planted in a granted folder, is
		// skipped; so is the protected path itself, whoever names it.
		if real == p || (!named && contains(p, real)) {
			return nil
		}
	}
	info, err := os.Stat(real)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		for _, p := range r.protect {
			if contains(real, p) {
				return r.grantEntries(real, write)
			}
		}
	} else if !named && !info.Mode().IsRegular() {
		return nil // sockets, FIFOs and devices found while carving are not granted
	}
	mask := r.access.dirRO
	switch {
	case info.IsDir() && write:
		mask = r.access.dirRW
	case !info.IsDir() && write:
		mask = r.access.fileRW
	case !info.IsDir():
		mask = r.access.fileRO
	}
	return r.addRule(real, mask)
}

func (r *ruleset) grantEntries(dir string, write bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := r.grantPath(realPath(filepath.Join(dir, e.Name())), write, false); err != nil {
			return err
		}
	}
	return nil
}

func (r *ruleset) addRule(path string, mask uint64) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EACCES) {
			return nil
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer unix.Close(fd)
	attr := unix.LandlockPathBeneathAttr{Allowed_access: mask & r.access.handled, Parent_fd: int32(fd)}
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(r.fd), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&attr)), 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("allow %s: %w", path, errno)
	}
	return nil
}

// restrictAndExec builds the domain for p, enters it and execs argv. Landlock
// and no_new_privs apply to the calling thread, so the thread that restricts
// itself is the one that calls execve.
func restrictAndExec(p Policy, argv []string) error {
	runtime.LockOSThread()
	state := probe()
	if state.Mode != ModeLandlock {
		return errors.New(state.Reason)
	}
	program := argv[0]
	if !filepath.IsAbs(program) {
		return fmt.Errorf("program %q is not an absolute path", program)
	}
	if err := p.sealedConflict(defaultWritable()); err != nil {
		return err
	}
	acc := accessFor(state.ABI)
	attr := unix.LandlockRulesetAttr{Access_fs: acc.handled}
	if state.ABI >= 6 {
		// The child may not signal or reach abstract sockets of processes
		// outside its own domain (the engine).
		attr.Scoped = unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET | unix.LANDLOCK_SCOPE_SIGNAL
	}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("create the Landlock ruleset: %w", errno)
	}
	r := &ruleset{fd: int(fd), access: acc}
	for _, path := range p.Protect {
		r.protect = append(r.protect, realPath(path))
	}
	for _, path := range p.ReadWrite {
		if err := r.grant(path, true); err != nil {
			return err
		}
	}
	readOnly := append(append([]string{}, p.ReadOnly...), p.Sealed...)
	readOnly = append(readOnly, systemReadOnly...)
	// The program itself, where it is installed, and the engine binary (hooks
	// the tool runs call it back).
	readOnly = append(readOnly, filepath.Dir(program), filepath.Dir(realPath(program)))
	if self, err := os.Executable(); err == nil {
		readOnly = append(readOnly, self)
	}
	for _, path := range readOnly {
		if err := r.grant(path, false); err != nil {
			return err
		}
	}
	for _, path := range deviceFiles {
		if err := r.addRule(path, acc.fileRW); err != nil {
			return err
		}
	}
	for _, path := range deviceDirs {
		if err := r.grant(path, true); err != nil {
			return err
		}
	}
	if err := applySessionLimits(); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs: %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0); errno != 0 {
		return fmt.Errorf("enter the Landlock domain: %w", errno)
	}
	_ = unix.Close(int(fd))
	return syscall.Exec(program, argv, os.Environ()) // #nosec G204 -- the program the engine resolved for this session
}

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
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

var landlockABI = func() (int, error) {
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
	fd, err := unix.Openat2(unix.AT_FDCWD, real, &unix.OpenHow{
		Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if errors.Is(err, unix.ELOOP) {
			// Check only for an absent target; never grant a reopened path.
			if _, statErr := os.Stat(real); errors.Is(statErr, os.ErrNotExist) {
				return nil
			}
		}
		return fmt.Errorf("open confined path %s without symlinks: %w", real, err)
	}
	file := os.NewFile(uintptr(fd), real)
	defer file.Close()
	for _, p := range r.protect {
		// An entry found while carving (not named by the policy) that resolves
		// into a protected path, e.g. a symlink planted in a granted folder, is
		// skipped; so is the protected path itself, whoever names it.
		if real == p || (!named && contains(p, real)) {
			return nil
		}
	}
	info, err := file.Stat()
	if err != nil {
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
	return r.addRuleHandle(int(file.Fd()), real, mask)
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
	return r.addRuleHandle(fd, path, mask)
}

func (r *ruleset) addRuleHandle(fd int, path string, mask uint64) error {
	attr := unix.LandlockPathBeneathAttr{Allowed_access: mask & r.access.handled, Parent_fd: int32(fd)}
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(r.fd), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&attr)), 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("allow %s: %w", path, errno)
	}
	return nil
}

// restrictAndExec builds the domain for p, enters it and execs argv. Landlock,
// no_new_privs and capabilities apply to the calling thread, so the thread that
// restricts itself is the one that calls execve.
func restrictAndExec(p Policy, program string, argv []string) error {
	runtime.LockOSThread()
	state := probe()
	if err := p.validateHeldABI(state); err != nil {
		return err
	}
	if state.Mode != ModeLandlock {
		return errors.New(state.Reason)
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
	for _, file := range p.SealedFiles {
		for _, protected := range r.protect {
			if contains(file.Name(), protected) || contains(protected, file.Name()) {
				_ = file.Close()
				return errors.New("confine: a validated read-only directory overlaps a protected engine folder")
			}
		}
		err := r.addRuleHandle(int(file.Fd()), file.Name(), acc.dirRO)
		// ExtraFiles survive the first exec. Close before the tool's exec so it
		// inherits the Landlock grant, never an unrestricted directory handle.
		_ = file.Close()
		if err != nil {
			return err
		}
	}
	for _, path := range p.ReadWrite {
		if err := r.grant(path, true); err != nil {
			return err
		}
	}
	readOnly := append(append([]string{}, p.ReadOnly...), p.Sealed...)
	for _, path := range readOnly {
		if err := r.grant(path, false); err != nil {
			return err
		}
	}
	for _, device := range p.Devices {
		mask := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE)
		if device.Write {
			mask |= unix.LANDLOCK_ACCESS_FS_WRITE_FILE | (acc.fileRW & unix.LANDLOCK_ACCESS_FS_TRUNCATE)
		}
		if device.Execute {
			mask |= unix.LANDLOCK_ACCESS_FS_EXECUTE
		}
		if device.IOCTL {
			mask |= acc.fileRW & unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
		}
		if err := r.addRule(realPath(device.Path), mask); err != nil {
			return err
		}
	}
	if p.Limits != nil {
		if err := applyLimits(*p.Limits); err != nil {
			return err
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs: %w", err)
	}
	if err := DropCapabilities(false); err != nil {
		return err
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0); errno != 0 {
		return fmt.Errorf("enter the Landlock domain: %w", errno)
	}
	_ = unix.Close(int(fd))
	if p.Ready != nil {
		unix.CloseOnExec(int(p.Ready.Fd()))
		if _, err := p.Ready.WriteString("ready\n"); err != nil {
			return fmt.Errorf("signal confined launch: %w", err)
		}
	}
	return syscall.Exec(program, argv, os.Environ()) // #nosec G204 -- the caller resolved this original program path; no shell
}

// DropCapabilities clears the calling thread's permitted, effective, inheritable
// and ambient capabilities, and empties its bounding set when it holds
// CAP_SETPCAP (a root engine, or the network helper in its user namespace).
// Without CAP_SETPCAP the bounding set cannot change: requireBoundingSet then
// refuses, otherwise no_new_privs keeps any later exec from regaining the
// cleared sets. The caller execs from this locked thread.
func DropCapabilities(requireBoundingSet bool) error {
	runtime.LockOSThread()
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{}
	if err := unix.Capget(&header, &data[0]); err != nil {
		return fmt.Errorf("read capabilities: %w", err)
	}
	switch {
	case data[0].Effective&(1<<unix.CAP_SETPCAP) != 0:
		// The kernel, not this build, knows its last capability: EINVAL ends the set.
		for capability := 0; ; capability++ {
			err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(capability), 0, 0, 0)
			if errors.Is(err, unix.EINVAL) && capability > 0 {
				break
			}
			if err != nil {
				return fmt.Errorf("drop bounding capability %d: %w", capability, err)
			}
		}
	case requireBoundingSet:
		return errors.New("empty the capability bounding set: CAP_SETPCAP is not held")
	}
	data = [2]unix.CapUserData{}
	if err := unix.Capset(&header, &data[0]); err != nil {
		return fmt.Errorf("clear capabilities: %w", err)
	}
	return nil
}

func openDirectory(path string) (*os.File, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{
		Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// Read the kernel's label of this already-held object, never reopen the stored
// pathname. A moved directory could otherwise acquire write rights from a new
// ancestor: Landlock grants add up. A changed location refuses the launch.
func directoryHandleUnchanged(file *os.File) error {
	current, err := os.Readlink("/proc/self/fd/" + strconv.FormatUint(uint64(file.Fd()), 10))
	if err != nil || current != file.Name() {
		return errors.New("confine: a validated read-only directory moved; the session was not started")
	}
	return nil
}

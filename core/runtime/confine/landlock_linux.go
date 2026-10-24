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
	"strings"
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
	handled, dirRW, dirNoDelete, fileRW, dirRO, fileRO uint64
}

// grantKind says how far a path is opened: read and run, change, or change
// without the rights to remove, rename or move a name.
type grantKind int

const (
	grantRead grantKind = iota
	grantWrite
	grantNoDelete
)

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
	dirRW := handled &^ unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK &^ unix.LANDLOCK_ACCESS_FS_MAKE_CHAR
	return access{
		handled: handled,
		dirRW:   dirRW,
		// Removing a name needs REMOVE_FILE or REMOVE_DIR and moving one needs REFER
		// (a rename also removes the old name); creating and changing need none.
		dirNoDelete: dirRW &^ (unix.LANDLOCK_ACCESS_FS_REMOVE_FILE | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REFER),
		fileRW:      fileRW,
		dirRO:       fileRO | unix.LANDLOCK_ACCESS_FS_READ_DIR,
		fileRO:      fileRO,
	}
}

// ruleset collects path rules for one Landlock domain.
type ruleset struct {
	fd      int
	access  access
	protect []string
	paths   []string
}

// grant allows path (directory or file) with the rights for its kind. A path
// that contains a protected path is not granted as a whole: its entries are,
// recursively, except the protected ones. A protected path itself is never
// granted; a path under one is granted only when the policy names it (a tool
// install, an account home). A missing path is skipped.
func (r *ruleset) grant(path string, kind grantKind) error {
	real := realPath(path)
	before := len(r.paths)
	err := r.grantPath(real, kind, true)
	if err == nil && len(r.paths) > before && path != real {
		r.paths = append(r.paths, path)
	}
	return err
}

func (r *ruleset) grantPath(real string, kind grantKind, named bool) error {
	fd, err := openNoSymlinks(real, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if errors.Is(err, unix.ELOOP) {
			// Check only for an absent or unreachable target (a link into a folder only root may search, such as
			// /etc/grub2-efi.cfg into /boot/efi); never grant a reopened path. A loop still refuses the launch.
			if _, statErr := os.Stat(real); errors.Is(statErr, os.ErrNotExist) || errors.Is(statErr, os.ErrPermission) {
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
	if !info.IsDir() && !named && !info.Mode().IsRegular() {
		return nil // sockets, FIFOs and devices found while carving are not granted
	}
	return r.grantHeld(file, real, info.IsDir(), kind)
}

// grantHeld grants the held object at real, or, for a directory that contains a
// protected path, each of its entries.
func (r *ruleset) grantHeld(file *os.File, real string, dir bool, kind grantKind) error {
	if dir {
		for _, p := range r.protect {
			if contains(real, p) {
				return r.grantEntries(file, real, kind)
			}
		}
	}
	mask := r.access.dirRO
	switch {
	case dir && kind == grantWrite:
		mask = r.access.dirRW
	case dir && kind == grantNoDelete:
		mask = r.access.dirNoDelete
	case !dir && kind != grantRead:
		mask = r.access.fileRW
	case !dir:
		mask = r.access.fileRO
	}
	return r.addRuleHandle(int(file.Fd()), real, mask)
}

// grantEntries grants the entries of the held directory dir, except the protected
// ones. An entry's kind comes from the listing (d_type) and the entry is opened
// relative to the held directory, so a carved directory or regular file is never
// stat'ed: the engine needs no attribute access to types it does not otherwise use
// (an SELinux appliance labels /etc's entries with a dozen of them). A symlink is
// resolved and checked by grantPath, as a named path is; sockets, FIFOs and devices
// are not granted.
func (r *ruleset) grantEntries(held *os.File, dir string, kind grantKind) error {
	fd, err := unix.Openat(int(held.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("list confined folder %s: %w", dir, err)
	}
	list := os.NewFile(uintptr(fd), dir)
	entries, err := list.ReadDir(-1)
	_ = list.Close()
	if err != nil {
		return err
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		switch typ := e.Type(); {
		case typ&os.ModeSymlink != 0:
			err = r.grantPath(realPath(path), kind, false)
		case typ.IsDir() || typ.IsRegular():
			err = r.grantEntry(held, e.Name(), path, typ.IsDir(), kind)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// grantEntry opens the listed name relative to the held directory without
// following a symlink. A directory is opened O_DIRECTORY, so a name swapped for a
// link or a file since the listing is refused; a regular file swapped for a link
// yields the link's own inode, whose rule grants nothing, because Landlock checks
// the hierarchy of the file a path resolves to.
func (r *ruleset) grantEntry(held *os.File, name, path string, dir bool, kind grantKind) error {
	for _, p := range r.protect {
		if path == p || contains(p, path) {
			return nil
		}
	}
	flags := unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if dir {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(int(held.Fd()), name, flags, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil // removed since the listing
	}
	if err != nil {
		return fmt.Errorf("open confined path %s without symlinks: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	return r.grantHeld(file, path, dir, kind)
}

func (r *ruleset) addRule(path string, mask uint64) error {
	fd, err := unix.Open(realPath(path), unix.O_PATH|unix.O_CLOEXEC, 0)
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
	r.paths = append(r.paths, path)
	return nil
}

// restrictAndExec builds the domain for p, enters it and execs argv. Landlock,
// no_new_privs and capabilities apply to the calling thread, so the thread that
// restricts itself is the one that calls execve.
// Helper re-exec only. In the engine process a locked goroutine that exits would
// fire the parent-death signal of the short-lived children the engine starts (git;
// modules/sessions/procrunner_pdeathsig_linux.go). A session's guard ends its tree instead.
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
	attr := unix.LandlockRulesetAttr{Handled_access_fs: acc.handled}
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
		if err := r.grant(path, grantWrite); err != nil {
			return err
		}
	}
	for _, path := range p.NoDelete {
		if err := r.grant(path, grantNoDelete); err != nil {
			return err
		}
	}
	for _, path := range p.KeepWritable {
		if err := r.grant(path, grantWrite); err != nil {
			return err
		}
	}
	readOnly := append(append([]string{}, p.ReadOnly...), p.Sealed...)
	for _, path := range readOnly {
		if err := r.grant(path, grantRead); err != nil {
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
		if err := r.addRule(device.Path, mask); err != nil {
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
	if os.Geteuid() == 0 {
		// Check DAC after dropping capabilities, before Landlock can mask the
		// cause. The kernel honors supplementary groups and directory ACLs.
		for _, path := range append([]string{program}, r.paths...) {
			info, err := os.Stat(path)
			if err == nil && info.IsDir() {
				err = unix.Faccessat(unix.AT_FDCWD, path, unix.X_OK, unix.AT_EACCESS)
			}
			if errors.Is(err, unix.EACCES) {
				return fmt.Errorf("confine: path %q cannot be traversed without DAC override; use a root-owned data directory (the default or /var/lib/olivares) and ensure granted paths are searchable by the child", path)
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("check confined path %q after dropping capabilities: %w", path, err)
			}
		}
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
	err := syscall.Exec(program, argv, os.Environ()) // #nosec G204 -- the caller resolved this original program path; no shell
	return fmt.Errorf("execute %s in the confined domain: %w", program, err)
}

// DropCapabilities clears the calling thread's permitted, effective, inheritable
// and ambient capabilities, and empties its bounding set when it holds
// CAP_SETPCAP (a root engine, or the network helper in its user namespace).
// Without CAP_SETPCAP the bounding set cannot change: requireBoundingSet then
// refuses, otherwise no_new_privs keeps any later exec from regaining the
// cleared sets. The caller execs from this locked thread.
// Helper re-exec only. In the engine process a locked goroutine that exits would
// fire the parent-death signal of the short-lived children the engine starts (git;
// modules/sessions/procrunner_pdeathsig_linux.go). A session's guard ends its tree instead.
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
	fd, err := openNoSymlinks(path, unix.O_DIRECTORY)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// openNoSymlinks opens path O_PATH with flags and refuses a symlink in any of its names with ELOOP, as openat2's
// RESOLVE_NO_SYMLINKS does. systemd's RestrictSUIDSGID= (the engine's unit sets it) answers openat2 with ENOSYS so
// that programs use openat, whose flags it can filter (src/shared/seccomp-util.c, seccomp_restrict_sxid). Then the
// path is walked one name at a time from held descriptors, each name opened O_NOFOLLOW and checked before the next.
func openNoSymlinks(path string, flags int) (int, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{
		Flags: uint64(unix.O_PATH | unix.O_CLOEXEC | flags), Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
	if !errors.Is(err, unix.ENOSYS) {
		return fd, err
	}
	if path == "" {
		return -1, unix.ENOENT
	}
	start := "."
	if filepath.IsAbs(path) {
		start = "/"
	}
	fd, err = unix.Open(start, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	names := strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
	for i, name := range names {
		next, err := unix.Openat(fd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return -1, err
		}
		var st unix.Stat_t
		if err := unix.Fstat(next, &st); err != nil {
			_ = unix.Close(next)
			return -1, err
		}
		switch kind := st.Mode & unix.S_IFMT; {
		case kind == unix.S_IFLNK:
			_ = unix.Close(next)
			return -1, unix.ELOOP
		case kind != unix.S_IFDIR && (i < len(names)-1 || flags&unix.O_DIRECTORY != 0):
			_ = unix.Close(next)
			return -1, unix.ENOTDIR
		}
		fd = next
	}
	return fd, nil
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

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// probeMounts reports whether this host gives a child a user and mount
// namespace in which it may mount. It runs the helper: a host that allows the
// clone can still deny the mount (an AppArmor or seccomp policy on user
// namespaces), and only the mount itself shows it. Only a success is kept, so
// a probe that failed under load is tried again by the next launch.
var probeMounts = func() error {
	probeMu.Lock()
	defer probeMu.Unlock()
	if probeOK {
		return nil
	}
	if err := runMountsProbe(); err != nil {
		return err
	}
	probeOK = true
	return nil
}

var (
	probeMu sync.Mutex
	probeOK bool
)

func runMountsProbe() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	// The helper mounts this directory, so only this process can remove it.
	dir, err := os.MkdirTemp("", "olivares-mounts-probe-")
	if err != nil {
		return err
	}
	defer os.Remove(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, HelperArg, mountsProbeArg, dir) // #nosec G204 -- the engine re-executes itself with a fixed helper argument
	enterMountNamespace(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		if reason := strings.TrimSpace(string(out)); reason != "" {
			return fmt.Errorf("%w: %s", err, reason)
		}
		return err
	}
	return nil
}

// enterMountNamespace gives cmd its own user namespace (the caller's uid and gid
// mapped to themselves) and mount namespace. The two capabilities are held only
// until the helper has mounted: Exec and the network helper clear them.
func enterMountNamespace(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	attr := cmd.SysProcAttr
	attr.Cloneflags |= syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS
	attr.UidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}}
	attr.GidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}}
	attr.GidMappingsEnableSetgroups = false
	for _, capability := range []uintptr{unix.CAP_SYS_ADMIN, unix.CAP_SETPCAP} {
		if !slices.Contains(attr.AmbientCaps, capability) {
			attr.AmbientCaps = append(attr.AmbientCaps, capability)
		}
	}
}

// WrapMounts makes cmd, a session helper command (modules/sessions/confine),
// apply m before its Landlock stage. cmd.Args must be the helper's:
// [path, HelperArg, ...]. The first stage that still holds the capability calls
// ConsumeMounts; helpers that do not (Wrap's own, for plugin jails) never see
// the layer. On a host without namespaces it leaves cmd unchanged and returns an
// error wrapping ErrMountsUnavailable; any other error is the caller's fault.
func WrapMounts(cmd *exec.Cmd, m Mounts) error {
	if len(m.ReadOnly)+len(m.Pin) == 0 {
		return nil
	}
	if len(cmd.Args) < 3 || cmd.Args[1] != HelperArg {
		return errors.New("confine: mounts apply to the confinement helper command")
	}
	paths := append(append([]string{}, m.ReadOnly...), m.Pin...)
	for _, n := range m.Create {
		paths = append(paths, n.Path)
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("confine: %q is not an absolute path", path)
		}
	}
	if err := probeMounts(); err != nil {
		return fmt.Errorf("%w: %v", ErrMountsUnavailable, err)
	}
	// Nothing is made on a host that cannot guard it. A node that cannot be made
	// (a read-only repository) is one the child could not make either; the
	// mounts are then not applied at all, and the caller says so.
	for _, n := range m.Create {
		if err := create(n); err != nil {
			return fmt.Errorf("%w: cannot create %s: %v", ErrMountsUnavailable, n.Path, err)
		}
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	cmd.Args = append([]string{cmd.Args[0], HelperArg, mountsArg, string(payload)}, cmd.Args[2:]...)
	enterMountNamespace(cmd)
	return nil
}

// create makes n whole or not at all: git reads some of these names, and an
// empty commondir stops it. The file is written beside its name and linked in,
// which never replaces what is there.
func create(n Node) error {
	if n.Dir {
		err := os.Mkdir(n.Path, 0o755)
		if errors.Is(err, fs.ErrExist) {
			return nil
		}
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(n.Path), ".olivares-node-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(n.Content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), n.Path); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}

// IsMountsProbe reports whether args are the helper's namespace probe: the flag
// and the empty directory the probe may mount.
func IsMountsProbe(args []string) bool { return len(args) == 2 && args[0] == mountsProbeArg }

// RunMountsProbe does what a real run does, on the directory the probe names:
// make the mount namespace private, bind the directory and remount it read-only.
// A host that refuses any step refuses the probe, so a run that passes it can
// mount.
func RunMountsProbe(args []string) int {
	if err := applyMounts(Mounts{ReadOnly: []string{args[1]}}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 126
	}
	return 0
}

// ConsumeMounts applies the mount layer WrapMounts put in front of a helper's
// arguments and returns the arguments that follow it. Arguments without the
// layer come back unchanged, so the first stage that still holds the
// capability (the network supervisor or the filesystem helper) can call it.
func ConsumeMounts(args []string) ([]string, error) {
	if len(args) == 0 || args[0] != mountsArg {
		return args, nil
	}
	var m Mounts
	if len(args) < 2 {
		return nil, errors.New("mount layer: no payload")
	}
	if err := json.Unmarshal([]byte(args[1]), &m); err != nil {
		return nil, fmt.Errorf("mount layer: invalid payload: %w", err)
	}
	if err := applyMounts(m); err != nil {
		return nil, fmt.Errorf("mount layer: %w", err)
	}
	if err := dropSysAdmin(); err != nil {
		return nil, fmt.Errorf("mount layer: %w", err)
	}
	return args[2:], nil
}

func applyMounts(m Mounts) error {
	// A bind mount must not propagate to the host's mount namespace.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make the mount namespace private: %w", err)
	}
	for _, path := range m.Pin {
		if err := bindOnto(path, false); err != nil {
			return err
		}
	}
	for _, path := range m.ReadOnly {
		if err := bindOnto(path, true); err != nil {
			return err
		}
	}
	return nil
}

// bindOnto binds the directory or file at path onto itself, read-only when asked.
// It holds the node by descriptor (never a symlink) and mounts through
// /proc/self/fd, so a path swapped after the check is not the one mounted.
func bindOnto(path string, readOnly bool) error {
	node, err := openNode(path)
	if err != nil {
		return err
	}
	defer unix.Close(node)
	held := fmt.Sprintf("/proc/self/fd/%d", node)
	if err := unix.Mount(held, held, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind %s: %w", path, err)
	}
	if !readOnly {
		return nil
	}
	// The descriptor still names the mount below. Reopen the path to reach the
	// new one, and refuse to remount anything that is not a mount of its own:
	// that would make the whole filesystem read-only.
	top, err := openNode(path)
	if err != nil {
		return err
	}
	defer unix.Close(top)
	below, err := nodeID(node)
	if err != nil {
		return fmt.Errorf("identify %s: %w", path, err)
	}
	above, err := nodeID(top)
	if err != nil {
		return fmt.Errorf("identify %s: %w", path, err)
	}
	if below.ino != above.ino || below.mount == above.mount {
		return fmt.Errorf("bind %s: the new mount is not the one the path reaches", path)
	}
	var fs unix.Statfs_t
	if err := unix.Fstatfs(top, &fs); err != nil {
		return fmt.Errorf("read the mount flags of %s: %w", path, err)
	}
	flags := lockedFlags(uint64(fs.Flags))
	target := fmt.Sprintf("/proc/self/fd/%d", top)
	if err := unix.Mount("", target, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|flags, ""); err != nil {
		return fmt.Errorf("make %s read-only: %w", path, err)
	}
	return nil
}

func openNode(path string) (int, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("guard %s: %w", path, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("guard %s: %w", path, err)
	}
	if kind := st.Mode & unix.S_IFMT; kind != unix.S_IFDIR && kind != unix.S_IFREG {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("guard %s: not a directory or a regular file", path)
	}
	return fd, nil
}

type nodeIdentity struct {
	ino   uint64
	mount uint64
}

func nodeID(fd int) (nodeIdentity, error) {
	var stx unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_INO|unix.STATX_MNT_ID, &stx); err != nil {
		return nodeIdentity{}, err
	}
	if stx.Mask&unix.STATX_MNT_ID == 0 {
		return nodeIdentity{}, errors.New("this kernel does not report mount identifiers")
	}
	return nodeIdentity{ino: stx.Ino, mount: stx.Mnt_id}, nil
}

// lockedFlags are the mount flags a mount made in a user namespace must keep as
// its parent had them. statfs reports them as ST_* values, which are the MS_*
// values for all but relatime.
func lockedFlags(statfs uint64) uintptr {
	var flags uintptr
	for st, ms := range map[uint64]uintptr{
		unix.ST_NOSUID:     unix.MS_NOSUID,
		unix.ST_NODEV:      unix.MS_NODEV,
		unix.ST_NOEXEC:     unix.MS_NOEXEC,
		unix.ST_NOATIME:    unix.MS_NOATIME,
		unix.ST_NODIRATIME: unix.MS_NODIRATIME,
		unix.ST_RELATIME:   unix.MS_RELATIME,
	} {
		if statfs&st != 0 {
			flags |= ms
		}
	}
	if flags&(unix.MS_NOATIME|unix.MS_RELATIME) == 0 {
		// A strictatime mount: without this the kernel remounts it as relatime,
		// which differs from the locked flag and is refused.
		flags |= unix.MS_STRICTATIME
	}
	return flags
}

// dropSysAdmin removes CAP_SYS_ADMIN from the process once the mounts are made:
// what stays (the network supervisor, which serves requests the tool feeds it)
// must not be able to undo them. Capabilities are per thread, so it asks the
// runtime to change them on every thread; a build that links cgo cannot, and
// then only the calling thread is changed.
// ponytail: with cgo the supervisor's other threads keep it, and nothing the tool
// can reach uses them; the engine release is static. Revisit if that changes.
func dropSysAdmin() error {
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&header, &data[0]); err != nil {
		return fmt.Errorf("read capabilities: %w", err)
	}
	bit := uint32(1) << unix.CAP_SYS_ADMIN
	data[0].Effective &^= bit
	data[0].Permitted &^= bit
	data[0].Inheritable &^= bit
	_, _, errno := syscall.AllThreadsSyscall(unix.SYS_CAPSET, uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&data[0])), 0)
	switch errno {
	case 0:
		return nil
	case syscall.ENOTSUP:
		return unix.Capset(&header, &data[0])
	}
	return fmt.Errorf("drop CAP_SYS_ADMIN: %w", errno)
}

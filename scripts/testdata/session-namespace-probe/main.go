// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

// session-namespace-probe exercises the clone flags and mount calls used by the session
// helpers: the user and network namespace of an internal design note (not shipped), and the mount
// namespace in which a folder's Git hooks and configuration are bound read-only. It
// also checks what must stay refused: other namespace types, a filesystem-creating
// mount and a mount outside the new namespace.
// Build with CGO_ENABLED=0 and run as the Compose container's non-root user:
// docker compose run --rm --no-deps --entrypoint /project/namespace-probe olivares
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// namespaces are the kinds the child must not share with its parent.
var namespaces = []string{"user", "net", "mnt"}

func main() {
	if err := probe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func probe() error {
	if os.Getuid() != 65532 {
		return fmt.Errorf("probe must run as Compose UID 65532, got %d", os.Getuid())
	}
	if len(os.Args) == 2 && os.Args[1] == "--noop" {
		return nil
	}
	if len(os.Args) == 3+len(namespaces) && os.Args[1] == "--child" {
		return child(os.Args[2], os.Args[3:])
	}
	// The directory is made here so this namespace can remove it: the child's bind
	// mount is read-only and private to the child.
	dir, err := os.MkdirTemp("", "namespace-probe-")
	if err != nil {
		return fmt.Errorf("make the directory to guard: %w", err)
	}
	defer os.RemoveAll(dir)
	// What the profile must keep refusing, here where the process holds no capability.
	if err := syscall.Mount("tmpfs", dir, "tmpfs", 0, ""); err == nil {
		return errors.New("a filesystem-creating mount outside a namespace succeeded")
	}
	// A namespace type the profile still denies cannot be created through clone either.
	// Only the profile's clone mask refuses this one: with the user namespace the
	// process holds the capability the kernel asks for.
	if err := launch(syscall.CLONE_NEWUSER|syscall.CLONE_NEWUTS, "--noop"); !errors.Is(err, syscall.EPERM) {
		return fmt.Errorf("clone with a namespace type the profile denies: got %v, want EPERM", err)
	}
	args := []string{"--child", dir}
	for _, namespace := range namespaces {
		link, err := os.Readlink("/proc/self/ns/" + namespace)
		if err != nil {
			return fmt.Errorf("read parent %s namespace: %w", namespace, err)
		}
		args = append(args, link)
	}
	if err := launch(syscall.CLONE_NEWUSER|syscall.CLONE_NEWNET|syscall.CLONE_NEWNS, args...); err != nil {
		return fmt.Errorf("session user/network/mount namespace launch: %w", err)
	}
	fmt.Println("PASS non-root child entered separate user, network and mount namespaces and bound a directory read-only; denied mounts and namespaces stayed denied")
	return nil
}

// launch runs this binary with args in new namespaces of the given kinds, as a
// non-root session helper does: its own user maps to itself and keeps the three
// capabilities the helper needs.
func launch(flags uintptr, args ...string) error {
	cmd := exec.Command(os.Args[0], args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 flags,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
		// Linux capability numbers: CAP_NET_ADMIN (12), CAP_SETPCAP (8), CAP_SYS_ADMIN (21).
		AmbientCaps: []uintptr{12, 8, 21},
	}
	return cmd.Run()
}

// child checks that it left its parent's namespaces, then binds dir read-only
// inside the new mount namespace, as the Git guard does for hooks. It prints nothing
// on success: the parent reports the result once everything held.
func child(dir string, parent []string) error {
	for i, namespace := range namespaces {
		link, err := os.Readlink("/proc/self/ns/" + namespace)
		if err != nil {
			return fmt.Errorf("read child %s namespace: %w", namespace, err)
		}
		if link == parent[i] {
			return fmt.Errorf("child retained parent's %s namespace", namespace)
		}
	}
	// Mounts that create a filesystem stay denied even with CAP_SYS_ADMIN here: they
	// carry neither of the two flags the profile allows.
	if err := syscall.Mount("tmpfs", dir, "tmpfs", 0, ""); err == nil {
		return errors.New("a filesystem-creating mount in the child namespace succeeded")
	}
	if err := bindReadOnly(dir); err != nil {
		return err
	}
	if _, err := os.Create(filepath.Join(dir, "hook")); !errors.Is(err, syscall.EROFS) {
		return fmt.Errorf("write into the read-only bind: got %v, want EROFS", err)
	}
	return nil
}

// stRelatime is the ST_RELATIME bit statfs reports. The other statfs flags that
// matter here (nosuid, nodev, noexec, noatime, nodiratime) equal their MS_* values.
const stRelatime = 0x1000

// bindReadOnly makes dir read-only for this mount namespace only. The remount keeps
// the flags the parent mount locked (nosuid, nodev, noexec, atime): inside a user
// namespace the kernel refuses a remount that drops them.
func bindReadOnly(dir string) error {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(dir, &fs); err != nil {
		return fmt.Errorf("statfs %s: %w", dir, err)
	}
	const locked = syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC | syscall.MS_NOATIME | syscall.MS_NODIRATIME
	flags := uintptr(fs.Flags) & locked
	switch {
	case fs.Flags&stRelatime != 0:
		flags |= syscall.MS_RELATIME
	case flags&syscall.MS_NOATIME == 0:
		flags |= syscall.MS_STRICTATIME
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make the mount tree private: %w", err)
	}
	if err := syscall.Mount(dir, dir, "", syscall.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind %s: %w", dir, err)
	}
	if err := syscall.Mount("", dir, "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY|flags, ""); err != nil {
		return fmt.Errorf("remount %s read-only: %w", dir, err)
	}
	return nil
}

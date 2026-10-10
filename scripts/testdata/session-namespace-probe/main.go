// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

// session-namespace-probe exercises the clone flags used by an internal design note (not shipped)
// Build with CGO_ENABLED=0 and run as the Compose container's non-root user:
// docker compose run --rm --no-deps --entrypoint /project/namespace-probe olivares
package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

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
	if len(os.Args) == 4 && os.Args[1] == "--child" {
		for i, namespace := range []string{"user", "net"} {
			link, err := os.Readlink("/proc/self/ns/" + namespace)
			if err != nil {
				return fmt.Errorf("read child %s namespace: %w", namespace, err)
			}
			if link == os.Args[i+2] {
				return fmt.Errorf("child retained parent's %s namespace", namespace)
			}
		}
		fmt.Println("PASS non-root child entered separate user and network namespaces")
		return nil
	}
	args := []string{"--child"}
	for _, namespace := range []string{"user", "net"} {
		link, err := os.Readlink("/proc/self/ns/" + namespace)
		if err != nil {
			return fmt.Errorf("read parent %s namespace: %w", namespace, err)
		}
		args = append(args, link)
	}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
		// Linux capability numbers: CAP_NET_ADMIN (12), CAP_SETPCAP (8).
		AmbientCaps: []uintptr{12, 8},
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("session user/network namespace launch: %w", err)
	}
	return nil
}

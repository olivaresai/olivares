// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package egress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/olivaresai/olivares/sdk/netbind"
)

// PrepareContainer keeps the policy and credential guard in the engine. The caller
// mounts dir at the same path and runs args through its engine binary in a container
// with --network=none and all capabilities dropped. Only the relay opens Unix sockets;
// the tool inherits the native runner's socket restrictions before it executes.
func PrepareContainer(ctx context.Context, policy Policy, argv []string) (args []string, dir string, waitReady func() error, release func(), err error) {
	ds, err := destinations(policy)
	if err != nil {
		return nil, "", nil, nil, err
	}
	p, err := startProxy(ctx, ds, withCredential(policy))
	if err != nil {
		return nil, "", nil, nil, err
	}
	raw, err := netbind.Listen(ctx, "unix", filepath.Join(p.dir, "ready.sock"), netbind.Policy{Component: "sessions", Purpose: "container relay readiness"})
	if err != nil {
		p.close()
		return nil, "", nil, nil, err
	}
	listener := raw.(*net.UnixListener)
	release = func() { _ = listener.Close(); p.close() }
	req := helperSpec{Container: true, Socket: p.socket, CAPEM: p.caPEM, Policy: policy, ReadyFD: 3, Args: argv}
	payload, err := json.Marshal(req)
	if err != nil {
		release()
		return nil, "", nil, nil, err
	}
	waitReady = func() error {
		stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
		defer stop()

		deadline := time.Now().Add(30 * time.Second)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		if err := listener.SetDeadline(deadline); err != nil {
			return err
		}
		conn, err := listener.AcceptUnix()
		if err != nil {
			return err
		}
		defer conn.Close()
		stopRead := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stopRead()
		if err := conn.SetReadDeadline(deadline); err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(conn, 1024))
		if err != nil {
			return err
		}
		if string(data) != "ready\n" {
			return fmt.Errorf("container network relay did not report readiness: %s", strings.TrimSpace(string(data)))
		}
		return nil
	}
	return []string{"__olivares_confine", containerArg, string(payload)}, p.dir, waitReady, release, nil
}

func runContainerHelper(payload string) int {
	// The same-uid tool must not write into its unfiltered relay via procfs.
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		fmt.Fprintln(os.Stderr, "olivares: protect container relay:", err)
		return 126
	}

	var req helperSpec
	if err := json.Unmarshal([]byte(payload), &req); err != nil || !req.Container || req.Files != 0 || req.ReadyFD != 3 || len(req.Args) == 0 {
		fmt.Fprintln(os.Stderr, "olivares: invalid container relay request:", err)
		return 126
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(filepath.Dir(req.Socket), "ready.sock"), Net: "unix"})
	if err != nil {
		fmt.Fprintln(os.Stderr, "olivares: container relay readiness:", err)
		return 126
	}
	ready, err := conn.File()
	_ = conn.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "olivares: container relay readiness:", err)
		return 126
	}
	defer ready.Close()
	code, err := serveNamespace(req, ready)
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		if status, ok := exited.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return DieOfSignal(status.Signal())
		}
		return code
	}
	if err != nil {
		_, _ = fmt.Fprintf(ready, "container network relay failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "olivares: container network relay failed:", err)
	}
	return code
}

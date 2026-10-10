// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandboxrt

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"github.com/olivaresai/olivares/sdk/netbind"
)

// startProxyRelay transports bytes from the guest's sole mounted socket to the
// existing loopback proxy. It makes no destination or authorization decisions:
// HTTP and CONNECT both traverse the same per-job egress gate, unchanged.
func startProxyRelay(parent context.Context, path, proxyAddr string) (func(), error) {
	ln, err := netbind.Listen(parent, "unix", path, netbind.Policy{
		Component: "sandbox-runtime", Purpose: "guest proxy transport", Protected: true,
	})
	if err != nil {
		return nil, fmt.Errorf("sandboxrt: cannot bind guest proxy socket: %w", err)
	}
	// The parent bundle is 0700; only its owner and the guest's explicit socket
	// mount can reach this inode. The unprivileged guest must be able to connect.
	if err := os.Chmod(path, 0o666); err != nil {
		_ = ln.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			guest, err := ln.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer guest.Close()
				stopGuest := context.AfterFunc(ctx, func() { _ = guest.Close() })
				defer stopGuest()
				proxy, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxyAddr)
				if err != nil {
					return
				}
				defer proxy.Close()
				stopProxy := context.AfterFunc(ctx, func() { _ = proxy.Close() })
				defer stopProxy()
				done := make(chan struct{})
				go func() {
					_, _ = io.Copy(proxy, guest)
					_ = proxy.(*net.TCPConn).CloseWrite()
					close(done)
				}()
				_, _ = io.Copy(guest, proxy)
				_ = guest.Close()
				_ = proxy.Close()
				<-done
			}()
		}
	}()
	return func() { _ = ln.Close(); cancel(); workers.Wait() }, nil
}

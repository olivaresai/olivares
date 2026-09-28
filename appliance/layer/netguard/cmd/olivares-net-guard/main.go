// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"context"
	"fmt"
	"github.com/olivaresai/olivares/appliance/layer/netguard"
	"github.com/olivaresai/olivares/appliance/layer/portal"
	"net"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"
	"time"
)

func main() { os.Exit(run()) }
func run() int {
	if len(os.Args) == 2 && os.Args[1] == "initialize-runtime" {
		if err := netguard.InitializeRuntime(); err != nil {
			fmt.Fprintln(os.Stderr, "network runtime initialization refused")
			return 2
		}
		return 0
	}
	if len(os.Args) != 1 {
		return 2
	}
	account, err := user.Lookup("olivares-net-guard")
	if err != nil || account.Uid != strconv.Itoa(os.Geteuid()) || os.Geteuid() == 0 {
		return 1
	}
	if os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) || os.Getenv("LISTEN_FDS") != "1" {
		return 2
	}
	f := os.NewFile(3, "guard-socket")
	listener, err := net.FileListener(f)
	f.Close()
	if err != nil {
		return 2
	}
	defer listener.Close()
	selection, reason := portal.ReadSelection(portal.SelectionFile)
	if reason != "" || len(selection.ManagementInterfaces) == 0 {
		fmt.Fprintln(os.Stderr, "network management selection unavailable")
		return 1
	}
	bus, err := netguard.NewSystemTransport(selection.ManagementInterfaces)
	if err != nil {
		fmt.Fprintln(os.Stderr, "network owner identity unavailable")
		return 1
	}
	defer bus.Close()
	journal, err := netguard.OpenJournal("/var/lib/olivares-net-guard/acts")
	if err != nil {
		return 2
	}
	clock, err := netguard.NewBootClock()
	if err != nil {
		return 2
	}
	lock, err := netguard.NewFileLock()
	if err != nil {
		return 2
	}
	engine, err := netguard.NewEngine(netguard.Config{Bus: bus, Clock: clock, Journal: journal, Lock: lock, Restorer: &netguard.RootHelper{}})
	if err != nil {
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := engine.Tick(ctx); err != nil {
					fmt.Fprintln(os.Stderr, "network recovery remains unresolved")
				}
			}
		}
	}()
	if err := (netguard.Edge{Engine: engine}).Serve(ctx, listener); err != nil && ctx.Err() == nil {
		return 2
	}
	return 0
}

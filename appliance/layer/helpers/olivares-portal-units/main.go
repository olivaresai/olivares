// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// olivares-portal-units lists units, reads a unit's status and logs, and starts, stops,
// restarts, reloads, enables and disables units, for an invoker the kernel attests.
//
// Its socket unit, olivares-helper-units.socket, listens on /run/olivares-helpers/units.sock
// with Accept=yes and starts one instance per connection, as root, with the connection as its
// standard input and output. It reads one document, {"op", "unit", "lines", "operation_id"},
// takes no argument and no path, admits the invoker from the connection, refuses what the unit
// class table does not admit, and asks the service manager over its bus. The logs read runs
// journalctl with one fixed argument vector and no shell.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
	"github.com/olivaresai/olivares/appliance/layer/services"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	code := invocation.Serve(ctx, helper(dialSystemBus, services.ExecJournal), invocation.Linux{}, os.Args[1:], 0, os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// dialSystemBus opens the instance's private connection to the system bus.
func dialSystemBus() (services.Bus, func(), error) {
	bus, err := services.DialSystemBus()
	if err != nil {
		return nil, nil, err
	}
	return bus, func() { _ = bus.Close() }, nil
}

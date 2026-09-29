// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// olivares-appliance reads and plans through the fixed, authenticated local API.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/olivaresai/olivares/appliance/layer/hostops/console"
	"github.com/olivaresai/olivares/appliance/layer/portal/localclient"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dial := func() (console.Session, error) { return localclient.Dial() }
	read := func() (moduleSession, error) { return localclient.Dial() }
	code, handled := runService(os.Args[1:], os.Stdout, os.Stderr, dial, read)
	if !handled {
		code, handled = runStorage(os.Args[1:], os.Stdout, os.Stderr, read)
	}
	if !handled {
		code, handled = runFirewall(os.Args[1:], os.Stdout, os.Stderr, dial, read)
	}
	if !handled {
		code = console.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, dial)
	}
	stop()
	os.Exit(code)
}

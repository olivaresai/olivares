// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// olivares-portal-cert replaces the Appliance Console's TLS pair, for an invoker the kernel attests.
//
// Its socket unit, olivares-helper-cert.socket, listens on /run/olivares-helpers/cert.sock with
// Accept=yes and starts one instance per connection, as root, with the connection as its standard
// input and output. It reads one document and takes no argument and no path. generate creates a
// new self-signed pair whose names are the portal's current origin and addresses, which the helper
// reads itself, and installs it in /etc/olivares-portal; it admits the repair console on tty1 alone.
// begin issues a nonce under which the Appliance Console spools a certificate, and install
// installs the certificate the nonce names with the key a store reference names; neither admits
// an invoker until the console's certificate audience is adopted. No document carries a file name,
// a host name, an address or a key. Until the appliance's kernel is proven to give each instance
// its peer's pidfd, every subcommand is refused with pidfd_unproven.
package main

import (
	"context"
	"crypto/rand"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	code := invocation.Serve(ctx, helper(appliance()), invocation.Linux{}, os.Args[1:], 0, os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// appliance is the helper's environment on an installed appliance: the portal's TLS directory, the
// portal's certificate spool and the helper's state directory, which its template provides.
func appliance() env {
	return env{
		TLSDir:   "/etc/olivares-portal",
		SpoolDir: "/var/lib/olivares-portal/spool/cert",
		StateDir: "/var/lib/olivares-portal-cert",
		Facts:    liveFacts{},
		Now:      time.Now,
		Random:   rand.Reader,
	}
}

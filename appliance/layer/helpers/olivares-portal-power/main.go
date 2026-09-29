// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// olivares-portal-power reboots or shuts down this host, for an invoker the kernel attests.
//
// Its socket unit, olivares-helper-power.socket, listens on /run/olivares-helpers/power.sock
// with Accept=yes and starts one instance per connection, as root, with the connection as its
// standard input and output. It reads one document, {"verb": "reboot"} or {"verb": "shutdown"}
// with its operation id, takes no argument and no path, admits the invoker from the connection
// (the repair console on tty1, by account and by the unit and controlling terminal of the
// process the peer's pidfd names), and asks the service manager for the act with a fixed
// argument vector and no shell. It answers "performed" when the service manager accepted the
// request, which is not the host having gone down. Until the appliance's kernel is proven to
// give each instance its peer's pidfd, it refuses both acts with pidfd_unproven.
package main

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	code := invocation.Serve(ctx, helper(execRun), invocation.Linux{}, os.Args[1:], 0, os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// execRun runs argv with no shell and an empty environment.
func execRun(ctx context.Context, argv []string) error {
	// #nosec G204 -- The sole caller is helper: it clones one of the two literal powerArgv vectors; no request value becomes an argument.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = []string{}
	return cmd.Run()
}

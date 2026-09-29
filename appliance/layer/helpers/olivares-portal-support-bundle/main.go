// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// olivares-portal-support-bundle produces a support bundle, unprivileged, into its own spool.
//
// Its socket unit, olivares-helper-support-bundle.socket, listens on
// /run/olivares-helpers/support-bundle.sock with Accept=yes and starts one instance per
// connection as the helper's own static, non-login account olivares-support-bundle, whose state
// directory, /var/lib/olivares-support-bundle (mode 0700), holds its key and its acts/ and out/
// directories, each 0700. It reads one document, {"op": "produce", "operation_id": "<id>"} or
// {"op": "fetch", "nonce": "<nonce>"}, takes no argument and no path, and names every bundle in
// out/ from a nonce it issued itself under its own key.
//
// No invoker is admitted yet: the verb stays unavailable on every surface until the helper's
// consumer, peer, spool owner and path are admitted together as one row
// (helperschema.SupportBundleRules). The helper refuses every request until then.
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

// spoolDir is the helper's state directory, which its unit's StateDirectory= creates.
const spoolDir = "/var/lib/olivares-support-bundle"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	s := spool{dir: spoolDir, random: rand.Reader, facts: hostFacts("/", time.Now)}
	code := invocation.Serve(ctx, helper(s), invocation.Linux{}, os.Args[1:], 0, os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !unix

package main

import (
	"fmt"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// NO SILENT NO-OP ON A PLATFORM WITHOUT flock (C03-23).
//
// The release builds this repository produces are linux and darwin only
// (`.goreleaser.yaml`: `goos: [linux, darwin]`), and both satisfy the `unix` build
// constraint, so the flock implementation is what ships. This file exists so the package
// still COMPILES elsewhere — and the shape it takes there is a refusal, not a lock that
// returns nil and does nothing.
//
// A no-op stub would be the worse kind of defect: every caller would read as protected,
// every test of the locked path would pass, and the concurrent-install corruption this
// lock exists to prevent would be back with no symptom pointing at it. Refusing says the
// true thing — on this platform the in-place upgrade has no mutual exclusion, so it is not
// offered — and anyone who needs it can implement the primitive here and delete the
// refusal.
func lockUpgradeTarget(target string) (func(), error) {
	return nil, exitcode.New(exitcode.Conflict, fmt.Errorf(
		"in-place upgrade of %s is not available on this platform: it has no upgrade lock implementation, and two concurrent installs would overwrite each other's rollback backup\nthe published builds are linux and darwin; on any other platform upgrade by replacing the binary through your package manager or image",
		target))
}

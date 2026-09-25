// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"sync"
	"testing"
)

// Production argon2id parameters. m is the memory cost in KiB (64 MiB, well over
// the OWASP 19 MiB floor); t the time cost; p the parallelism. They are stored in
// each encoded hash so they can be raised later: VerifyPassword reads a hash's own
// parameters, and setting or resetting a password hashes it with the live ones.
//
// These constants are the production defaults. HashPassword reads the live copies
// (which start equal to these). Tests may lower the live copies via
// SetTestHashParams; they cannot change these constants, and nothing in
// production config or the environment can reach the live copies.
const (
	DefaultArgonMemKiB  uint32 = 64 * 1024
	DefaultArgonTime    uint32 = 3
	DefaultArgonThreads uint8  = 1

	// TestArgon* are the reduced parameters SetTestHashParams installs. They
	// remain a real argon2id PHC hash (VerifyPassword still works) but cost
	// milliseconds instead of ~1.5 s per login-pair under -race.
	TestArgonMemKiB  uint32 = 64 // 64 KiB
	TestArgonTime    uint32 = 1
	TestArgonThreads uint8  = 1

	argonSaltLen = 16
	argonKeyLen  = 32
)

var (
	argonMu      sync.Mutex
	argonMemKiB  = DefaultArgonMemKiB
	argonTime    = DefaultArgonTime
	argonThreads = DefaultArgonThreads
)

func currentArgonParams() (mem uint32, time uint32, threads uint8) {
	argonMu.Lock()
	defer argonMu.Unlock()
	return argonMemKiB, argonTime, argonThreads
}

// SetTestHashParams replaces the process-wide argon2id parameters used by
// HashPassword. It panics unless the running program is a `go test` binary
// (testing.Testing()); it is not reachable from production config or from an
// environment variable. VerifyPassword keeps using the parameters stored in
// each encoded hash, so production hashes still verify at production cost.
func SetTestHashParams(memKiB, timeCost uint32, threads uint8) {
	if !testing.Testing() {
		panic("auth.SetTestHashParams is test-only")
	}
	if memKiB == 0 || timeCost == 0 || threads == 0 {
		panic("auth.SetTestHashParams: argon2id parameters must be non-zero")
	}
	argonMu.Lock()
	argonMemKiB = memKiB
	argonTime = timeCost
	argonThreads = threads
	argonMu.Unlock()
	resetDummyHash()
}

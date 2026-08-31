// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package pgtest

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// deadDSN points at a port nothing listens on. 127.0.0.1:1 refuses immediately
// rather than timing out, so the negative control costs milliseconds and the test
// never depends on reachProbeTimeout actually elapsing.
const deadDSN = "postgres://postgres:postgres@127.0.0.1:1/postgres?sslmode=disable"

// TestTheOldGateCallsADeadServerAvailable is the mutation proof, and it needs no
// server of its own — which is the point, because the box that most needs to run it
// is the box with no Postgres.
//
// Until 2026-08-14 Available's whole body was: classify the env vars, parse the DSN,
// resolve the app role, return true. Every one of those steps SUCCEEDS for deadDSN.
// So the function called Available answered `true` for a server that does not exist,
// and the suites went on to fail deep inside provisioning, where the message names a
// transaction rather than a missing server.
//
// This test pins the two halves against each other: the old predicate still says yes,
// and the new one says no. If someone ever deletes the reachability probe, the first
// half keeps passing and the second fails, naming exactly what was removed.
func TestTheOldGateCallsADeadServerAvailable(t *testing.T) {
	t.Setenv(EnvSuperuserDSN, deadDSN)
	t.Setenv(EnvAppDSN, "postgres://olivares_app:pw@127.0.0.1:1/postgres?sslmode=disable")

	// --- the OLD predicate, reproduced exactly ---
	if got := classify(os.Getenv(EnvSuperuserDSN), os.Getenv(EnvAppDSN), os.Getenv(EnvAdminDSN)); got != gateRun {
		t.Fatalf("classify said %v, want gateRun: the fixture no longer reproduces the old gate", got)
	}
	if _, err := parseDSN(os.Getenv(EnvSuperuserDSN)); err != nil {
		t.Fatalf("parseDSN rejected the dead DSN (%v), so this no longer proves what it claims", err)
	}
	if _, err := appRole(); err != nil {
		t.Fatalf("appRole rejected the fixture (%v), so this no longer proves what it claims", err)
	}
	// Three greens. That WAS the entire answer, and the server is not there.

	// --- the NEW predicate, on the same DSN ---
	if err := reachErr(deadDSN); err == nil {
		t.Fatal("reachErr accepted a port nothing listens on: the probe is not connecting")
	}
}

// TestReachErrAnswersTheServerNotTheString covers the live side when a server is
// configured. It deliberately does NOT skip silently: when the harness says a server
// is available, this asserts the probe agrees, and when it says nothing is configured
// there is genuinely nothing to measure.
func TestReachErrAnswersTheServerNotTheString(t *testing.T) {
	super := os.Getenv(EnvSuperuserDSN)
	if super == "" {
		t.Skip("no superuser DSN configured: nothing to reach, and inventing one would measure this test's fixture")
	}
	if err := reachErr(super); err != nil {
		t.Fatalf("the configured server does not answer: %v", err)
	}
}

// TestReachErrCachesPerDSN pins the cache to the DSN rather than to the package, so
// two different servers in one binary cannot inherit each other's verdict.
func TestReachErrCachesPerDSN(t *testing.T) {
	start := time.Now()
	first := reachErr(deadDSN)
	second := reachErr(deadDSN)
	if first == nil || second == nil {
		t.Fatal("both calls must report the dead server")
	}
	if !errors.Is(second, first) && second.Error() != first.Error() {
		t.Fatalf("the second call returned a different verdict (%v vs %v): it is not cached", second, first)
	}
	if elapsed := time.Since(start); elapsed > reachProbeTimeout {
		t.Fatalf("two probes of a refusing port took %s: something is waiting instead of being refused", elapsed)
	}
	// A DIFFERENT dead DSN must be probed on its own, not served from the first entry.
	other := "postgres://postgres:postgres@127.0.0.1:2/postgres?sslmode=disable"
	if err := reachErr(other); err == nil {
		t.Fatal("a second, distinct dead DSN was accepted: the cache is keyed too widely")
	}
}

// TestPostgresRequiredRefusesToGuess covers the third answer of the declaration
// itself: a value nobody can read is a misconfiguration, not a "no". Reading it as
// false is how a CI run silently stops requiring what its author believed it required.
func TestPostgresRequiredRefusesToGuess(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    bool
		wantErr bool
	}{
		{raw: "", want: false},
		{raw: "1", want: true},
		{raw: "true", want: true},
		{raw: "TRUE", want: true},
		{raw: "  1  ", want: true}, // trimmed: a trailing space in a CI yaml is not a "no"
		{raw: "0", want: false},
		{raw: "false", want: false},
		{raw: "maybe", wantErr: true},
		{raw: "yes", wantErr: true}, // ParseBool rejects it, and guessing is what this refuses
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv(EnvRequired, tc.raw)
			got, err := postgresRequired()
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("%q was accepted as %v: the harness guessed", tc.raw, got)
			case !tc.wantErr && err != nil:
				t.Fatalf("%q was refused: %v", tc.raw, err)
			case !tc.wantErr && got != tc.want:
				t.Fatalf("%q read as %v, want %v", tc.raw, got, tc.want)
			case tc.wantErr && !strings.Contains(err.Error(), EnvRequired):
				t.Fatalf("the refusal does not name %s, so a reader cannot act on it: %v", EnvRequired, err)
			}
		})
	}
}

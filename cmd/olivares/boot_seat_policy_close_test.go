// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// ownedSeatPolicy stands for an edition policy that publishes process-scoped state
// when it is built and withdraws it when its engine closes. live is the number of
// such engines at this moment.
type ownedSeatPolicy struct {
	auth.SeatPolicy
	live *int
}

func (p ownedSeatPolicy) Close() error {
	*p.live--
	return nil
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// ownedSeatEdition swaps in a seat policy that owns process state, and a module
// binding that returns one other edition resource, for the length of the test. It
// returns the counters the cases read: engines currently owning state, policies
// built, and how many engines owned state when the other resource closed.
func ownedSeatEdition(t *testing.T) (live, built *int, liveWhenOtherClosed *[]int) {
	t.Helper()
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	live, built, liveWhenOtherClosed = new(int), new(int), new([]int)
	thisEdition.seatPolicy = func(*licenseHolder, crlViewFunc) auth.SeatPolicy {
		if *live != 0 {
			t.Errorf("boot %d built its seat policy while %d earlier engine(s) still owned process state", *built+1, *live)
		}
		*built++
		*live++
		return ownedSeatPolicy{SeatPolicy: auth.NewCommunitySeatPolicy(), live: live}
	}
	thisEdition.bindModuleDependencies = func(context.Context, EditionConfig, []api.Module, EditionDependencies, *slog.Logger) ([]io.Closer, error) {
		return []io.Closer{closerFunc(func() error {
			*liveWhenOtherClosed = append(*liveWhenOtherClosed, *live)
			return nil
		})}, nil
	}
	return live, built, liveWhenOtherClosed
}

// TestBootClosesTheSeatPolicyWithItsEngine: one command may boot one engine after
// another (dr drill seeds, backs up and restores in one process). Each engine must
// end what its seat policy published before the next one publishes its own, or a
// process-scoped source would have to refuse the second boot. The policy closes
// after the engine's other edition resources, so they shut down while it is live.
func TestBootClosesTheSeatPolicyWithItsEngine(t *testing.T) {
	live, built, liveWhenOtherClosed := ownedSeatEdition(t)

	for i := 1; i <= 2; i++ {
		eng, err := boot(context.Background(), bootConfig{
			DataDir: t.TempDir(), Engine: "sqlite", Version: "test", Logger: discardLogger(),
		})
		if err != nil {
			t.Fatalf("boot %d: %v", i, err)
		}
		if *live != 1 {
			t.Fatalf("boot %d: %d engines own process state while it is open, want 1", i, *live)
		}
		if err := eng.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
		if *live != 0 {
			t.Fatalf("close %d left %d engine(s) owning process state", i, *live)
		}
	}
	if *built != 2 {
		t.Fatalf("seat policy built %d times, want 2", *built)
	}
	if got := *liveWhenOtherClosed; len(got) != 2 || got[0] != 1 || got[1] != 1 {
		t.Fatalf("the engine's other resources closed with %v engine(s) owning process state, want [1 1]: "+
			"the seat policy must close after them", got)
	}
}

// TestFailedBootClosesTheSeatPolicy: a boot that fails after the seat seam has built
// the policy must end it too, or the next boot in the process would be refused by a
// source nobody serves from.
func TestFailedBootClosesTheSeatPolicy(t *testing.T) {
	live, built, _ := ownedSeatEdition(t)
	bad := filepath.Join(t.TempDir(), "hook-pep.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_HOOK_PEP_CONFIG", bad)

	eng, err := boot(context.Background(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Version: "test", ServeMode: true, Logger: discardLogger(),
	})
	if eng != nil || err == nil {
		t.Fatalf("boot with a malformed hook config = engine:%v err:%v, want nil engine and the refusal", eng, err)
	}
	if *built != 1 {
		t.Fatalf("the boot failed before the seat seam (policy built %d times); the case does not reach what it names", *built)
	}
	if *live != 0 {
		t.Fatalf("a failed boot left %d engine(s) owning process state", *live)
	}
}

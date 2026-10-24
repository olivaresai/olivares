// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
)

// The outbox nudge belongs to each engine's sessions module. With one process-wide slot,
// a second engine booted in the same process took the wake-up, and the first engine's outbox rows waited for
// its 5 minute safety tick.
func TestAnOutboxInsertWakesItsOwnEngineWhileAnotherEngineRuns(t *testing.T) {
	e := bootMessagingEstate(t, "sqlite")
	before := communicationHTTPTestOutboxStates(t, e.eng, e.tT)

	// A second engine in this process registers its own pump after this one.
	prepareCompositionTestBoot(t)
	other, err := boot(context.Background(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Version: "test", ServeMode: true,
	})
	if err != nil {
		t.Fatalf("boot the other engine: %v", err)
	}
	t.Cleanup(func() { _ = other.Close() })

	// One more notice writes one more outbox row in this engine's store; this engine's pump must publish it.
	e.sendNotice(e.comm.control.id)
	eventually(t, "this engine's own outbox publication while another engine runs", func() bool {
		states := communicationHTTPTestOutboxStates(t, e.eng, e.tT)
		return len(states) == 1 && states["published"] > before["published"]
	})

	// Closing the other engine withdraws only its own nudge.
	if err := other.Close(); err != nil {
		t.Fatalf("close the other engine: %v", err)
	}
	if !e.eng.sessionsMod.WorkOutboxNudgeRegistered() {
		t.Fatal("closing another engine withdrew this engine's outbox nudge")
	}
}

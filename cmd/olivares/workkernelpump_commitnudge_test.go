// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"sync/atomic"
	"testing"
)

// The outbox nudge must follow the commit of the row it announces. The pump's drain runs in its own
// transaction, so a drain woken before the commit cannot see the row under PostgreSQL's read
// committed isolation: it finds nothing, holds nothing back and idles, and the row waits for the
// safety tick. The drain starts at the nudge, so this test reads the outbox from outside the
// inserting transaction at that instant and requires the announced row to be there. SQLite has one
// writer, so there the read waits for the open transaction and a nudge at the insert shows as a
// 503 on the request instead.
func TestAnOutboxNudgeFindsTheRowItAnnouncesCommitted(t *testing.T) {
	for _, engineName := range []string{"sqlite", "postgres"} {
		t.Run(engineName, func(t *testing.T) {
			e := bootMessagingEstate(t, engineName)
			seen := communicationHTTPTestOutboxStates(t, e.eng, e.tT)["pending"]
			var nudges, early atomic.Int64
			e.eng.sessionsMod.SetWorkOutboxNudge(func() {
				nudges.Add(1)
				pending := communicationHTTPTestOutboxStates(t, e.eng, e.tT)["pending"]
				if pending <= seen {
					early.Add(1)
				}
				seen = pending
			})

			e.sendNotice(e.comm.control.id)

			if nudges.Load() == 0 {
				t.Fatal("the notice's outbox insert did not nudge the pump")
			}
			if early.Load() != 0 {
				t.Fatalf("%d of %d nudge(s) came before the row they announce was committed", early.Load(), nudges.Load())
			}
		})
	}
}

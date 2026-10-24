// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// gatedFencer refuses the durable fence while refuse is set, as a loaded store whose fence query
// fails does, and counts how often the K3 lane asked it.
type gatedFencer struct {
	refuse atomic.Bool
	asked  atomic.Int64
}

func (f *gatedFencer) FencedEpoch(context.Context) (uint64, error) {
	f.asked.Add(1)
	if f.refuse.Load() {
		return 0, errors.New("fence query failed")
	}
	return 1, nil
}

// A communication row whose nudged drain found the K3 lane refusing is held, not lost. When the
// lane allows again, the row must publish within the messaging tests' 10 s, not at the pump's
// 5 minute safety tick: an insert is the only other wake-up, and a held row gets no second one.
func TestAHeldCommunicationRowPublishesWhenTheLaneAllowsAgain(t *testing.T) {
	e := bootMessagingEstate(t, "sqlite")
	witness, _ := e.eng.communicationPump.authority.lane()
	if witness == nil {
		t.Fatal("the messaging estate has no K3 lane witness")
	}
	gate := &gatedFencer{}
	gate.refuse.Store(true)
	witness.mu.Lock()
	witness.fencer = gate
	witness.mu.Unlock()
	before := communicationHTTPTestOutboxStates(t, e.eng, e.tT)

	// The notice's insert nudges the pump; its drain asks the fence, which refuses.
	e.sendNotice(e.comm.control.id)
	eventually(t, "the nudged drain asks the refusing fence", func() bool { return gate.asked.Load() > 0 })
	if states := communicationHTTPTestOutboxStates(t, e.eng, e.tT); states["published"] != before["published"] {
		t.Fatalf("a row published through a refusing fence: before %v, now %v", before, states)
	}

	gate.refuse.Store(false)
	eventually(t, "the held row's publication once the lane allows again", func() bool {
		states := communicationHTTPTestOutboxStates(t, e.eng, e.tT)
		return len(states) == 1 && states["published"] > before["published"]
	})
}

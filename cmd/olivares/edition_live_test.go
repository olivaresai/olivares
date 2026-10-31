// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
)

type liveTestGate struct{ name string }

func (liveTestGate) Inspect() {}

type liveTestInspector interface{ Inspect() }

type liveFakeInspector struct{}

func (liveFakeInspector) Inspect(context.Context, claudeapi.ContentInspectionInput) claudeapi.ContentInspectionDecision {
	return claudeapi.ContentInspectionDecision{Forward: true}
}

func TestEditionLiveIsNotConfiguredUntilSomethingIsSet(t *testing.T) {
	var cell editionLive[liveTestInspector]
	if got := cell.Get(); got != nil {
		t.Fatalf("a cell nobody set answers %v; it must be the nil interface a consumer checks for", got)
	}
}

func TestEditionLiveBootBuildsOnceHoweverManyConsumersAsk(t *testing.T) {
	var cell editionLive[liveTestInspector]
	var builds atomic.Int32
	build := func() liveTestInspector { builds.Add(1); return liveTestGate{"boot"} }

	first, second := cell.bootFrom(build), cell.bootFrom(build)
	if builds.Load() != 1 {
		t.Fatalf("boot gate built %d times; every consumer must share one", builds.Load())
	}
	cell.Set(liveTestGate{"saved"})
	for name, get := range map[string]func() liveTestInspector{"first": first, "second": second} {
		if got := get(); got != (liveTestGate{"saved"}) {
			t.Errorf("%s consumer sees %v after a save; want the saved gate", name, got)
		}
	}
}

// The edition applies stored settings when the store opens, before the servers ask for their
// gates: what an administrator saved is newer than what the environment built at boot.
func TestEditionLiveSettingAppliedBeforeTheFirstConsumerBeatsTheBootValue(t *testing.T) {
	var cell editionLive[liveTestInspector]
	cell.Set(liveTestGate{"saved"})
	get := cell.bootFrom(func() liveTestInspector { return liveTestGate{"boot"} })
	if got := get(); got != (liveTestGate{"saved"}) {
		t.Fatalf("consumer sees %v; the setting applied before it asked must win", got)
	}
	cell.Clear()
	late := cell.bootFrom(func() liveTestInspector { return liveTestGate{"boot"} })
	if got := late(); got != nil {
		t.Fatalf("a saved \"off\" must stay off for a consumer that asks later; sees %v", got)
	}
}

// A save that turns the gate off must reach the consumer as the nil interface: its nil
// checks decide whether a response is buffered and whether a tool call is governed.
func TestEditionLiveClearAnswersTheNilInterface(t *testing.T) {
	var cell editionLive[liveTestInspector]
	cell.Set(liveTestGate{"on"})
	cell.Clear()
	if got := cell.Get(); got != nil {
		t.Fatalf("cleared cell answers %v; want the nil interface", got)
	}
}

// What the boot constructor returns when the edition has no configuration is the nil
// interface; seeding with it must keep the consumer's "not configured" meaning.
func TestEditionLiveBootFromNilKeepsNotConfigured(t *testing.T) {
	var cell editionLive[liveTestInspector]
	get := cell.bootFrom(func() liveTestInspector { return nil })
	if got := get(); got != nil {
		t.Fatalf("seeded from nil, answers %v", got)
	}
}

// Two engines in one process (every test, and `dr drill`) must not share a gate.
func TestEditionLiveGatesOfTwoEnginesAreIndependent(t *testing.T) {
	a, b := &editionLiveGates{}, &editionLiveGates{}
	a.hookFirewall.Set(liveFakeInspector{})
	if b.hookFirewall.Get() != nil {
		t.Fatal("a gate saved on one engine reached another engine in the same process")
	}
}

func TestEditionLiveIsSafeUnderConcurrentUse(t *testing.T) {
	var cell editionLive[liveTestInspector]
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				cell.Set(liveTestGate{"x"})
				cell.Clear()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if g := cell.Get(); g != nil {
					g.Inspect()
				}
			}
		}()
	}
	wg.Wait()
}

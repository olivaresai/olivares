// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// switchableThreatIntel is a threat-intel source that can be turned on while the
// engine runs: it reports itself off (sdk.CapabilityEnabled) until it is.
type switchableThreatIntel struct {
	threatIntelSource
	on       atomic.Bool
	inspects atomic.Int32
}

func (s *switchableThreatIntel) CapabilityEnabled() bool      { return s.on.Load() }
func (s *switchableThreatIntel) StartRefresh(context.Context) {}
func (s *switchableThreatIntel) InspectObserved(string, string, string, time.Time) []sdkmodel.FindingReport {
	s.inspects.Add(1)
	return nil
}

// The subscription is made once at boot; a source configured later must be served by
// it, and one that is off must be asked nothing.
func TestThreatIntelFollowsTheSourceBeingTurnedOnLater(t *testing.T) {
	src := &switchableThreatIntel{}
	prev := thisEdition.threatIntelSource
	thisEdition.threatIntelSource = func(func(string) string, *slog.Logger) threatIntelSource { return src }
	t.Cleanup(func() { thisEdition.threatIntelSource = prev })

	bus := eventbus.NewInProc(eventbus.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(func() { _ = bus.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	subscribeThreatIntel(ctx, func(string) string { return "" }, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))

	publish := func() {
		t.Helper()
		if err := bus.Publish(ctx, event.GuardrailObserved("tenant", "test", event.ObservedText{Surface: "input", Text: "x"})); err != nil {
			t.Fatal(err)
		}
	}
	waitFor := func(want int32) bool {
		for range 50 {
			if src.inspects.Load() == want {
				return true
			}
			time.Sleep(20 * time.Millisecond)
		}
		return src.inspects.Load() == want
	}
	publish()
	time.Sleep(200 * time.Millisecond)
	if got := src.inspects.Load(); got != 0 {
		t.Fatalf("a source that is off was asked %d times, want 0", got)
	}
	src.on.Store(true)
	publish()
	if !waitFor(1) {
		t.Fatalf("a source turned on later was asked %d times, want 1", src.inspects.Load())
	}
}

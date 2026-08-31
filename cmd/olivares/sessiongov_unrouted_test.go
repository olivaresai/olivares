// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// captureSink records the events published to it (a fake observationSink).
type captureSink struct{ events []event.Event }

func (s *captureSink) Publish(_ context.Context, e event.Event) error {
	s.events = append(s.events, e)
	return nil
}

func plainLaunch() sessions.LaunchIntent {
	return sessions.LaunchIntent{Transport: sessions.TransportStreamJSON, PermissionMode: "default"}
}

// an authorized launch whose inference is NOT routed through the governing proxy
// raises a posture finding — the model-layer governance gap is made loud and evidenced.
func TestSessionLaunchGate_UnroutedEmitsPostureFinding(t *testing.T) {
	sink := &captureSink{}
	g := &sessionLaunchGate{recordAvailable: true, inferenceRouted: false, sink: sink, log: slog.Default()}

	dec, err := g.Authorize(context.Background(), "t1", plainLaunch())
	if err != nil || !dec.Allowed {
		t.Fatalf("plain launch must be allowed, got allowed=%v err=%v", dec.Allowed, err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("an un-routed launch must emit exactly one posture finding, got %d", len(sink.events))
	}
	e := sink.events[0]
	if e.Type != event.TypeFindingReported || e.Source != sessionSignalSource {
		t.Errorf("finding event has wrong type/source: %s / %s", e.Type, e.Source)
	}
	fr, ok := e.Payload.(sdkmodel.FindingReport)
	if !ok {
		t.Fatalf("payload is not a FindingReport: %T", e.Payload)
	}
	if fr.Kind != unroutedLaunchFindingKind {
		t.Errorf("finding kind = %q, want %q", fr.Kind, unroutedLaunchFindingKind)
	}
	if fr.Severity != sdkmodel.SeverityMedium {
		t.Errorf("finding severity = %q, want medium", fr.Severity)
	}
	if fr.DetailHash == "" {
		t.Error("finding must carry a DetailHash (per-tenant dedup key)")
	}
}

// A routed launch is fully governed in-band by the proxy — no posture finding.
func TestSessionLaunchGate_RoutedEmitsNoFinding(t *testing.T) {
	sink := &captureSink{}
	g := &sessionLaunchGate{recordAvailable: true, inferenceRouted: true, sink: sink, log: slog.Default()}
	if dec, err := g.Authorize(context.Background(), "t1", plainLaunch()); err != nil || !dec.Allowed {
		t.Fatalf("routed launch must be allowed, got allowed=%v err=%v", dec.Allowed, err)
	}
	if len(sink.events) != 0 {
		t.Errorf("a routed launch must emit no posture finding, got %d", len(sink.events))
	}
}

// The finding is deduped PER TENANT: two tenants launching un-routed produce two distinct
// DetailHashes (so a SIEM sees one active finding per affected tenant, not a global one).
func TestSessionLaunchGate_UnroutedFindingIsPerTenant(t *testing.T) {
	sink := &captureSink{}
	g := &sessionLaunchGate{recordAvailable: true, inferenceRouted: false, sink: sink, log: slog.Default()}
	_, _ = g.Authorize(context.Background(), "tenant-a", plainLaunch())
	_, _ = g.Authorize(context.Background(), "tenant-b", plainLaunch())
	if len(sink.events) != 2 {
		t.Fatalf("want two findings, got %d", len(sink.events))
	}
	a := sink.events[0].Payload.(sdkmodel.FindingReport).DetailHash
	b := sink.events[1].Payload.(sdkmodel.FindingReport).DetailHash
	if a == b {
		t.Error("per-tenant DetailHash must differ across tenants")
	}
}

// A nil sink (no bus wired) never panics and never blocks the launch.
func TestSessionLaunchGate_UnroutedNilSinkNoPanic(t *testing.T) {
	g := &sessionLaunchGate{recordAvailable: true, inferenceRouted: false, sink: nil, log: slog.Default()}
	if dec, err := g.Authorize(context.Background(), "t1", plainLaunch()); err != nil || !dec.Allowed {
		t.Fatalf("launch must be allowed with no sink, got allowed=%v err=%v", dec.Allowed, err)
	}
}

// A DENIED launch (budget cap) must NOT emit the un-routed finding — the finding is only for
// a session that actually launches.
func TestSessionLaunchGate_DeniedLaunchEmitsNoFinding(t *testing.T) {
	sink := &captureSink{}
	g := &sessionLaunchGate{
		fin:             fakeBudget{chk: finops.BudgetCheck{Allowed: false, Action: "block"}},
		recordAvailable: true, inferenceRouted: false, sink: sink, log: slog.Default(),
	}
	dec, err := g.Authorize(context.Background(), "t1", plainLaunch())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Allowed {
		t.Fatal("a budget-capped launch must be denied")
	}
	if len(sink.events) != 0 {
		t.Errorf("a denied launch must not emit the un-routed finding, got %d", len(sink.events))
	}
}

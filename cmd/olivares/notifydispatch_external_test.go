// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secure/modelsign"
	"github.com/olivaresai/olivares/modules/notify"
	"github.com/olivaresai/olivares/sdk"
)

// These tests pin the external output-plugin composition: the SAME deny-closed
// admission gate as external sources (reusing newExtFixture from externalplugins_test),
// plus the atomic-reload/readiness/reconcile lifecycle — WITHOUT launching a
// subprocess, by injecting the verified-dispense seam (dispenseVerified). The real
// exec-time checksum pin is covered at the runtime layer (loader_test.go).

// stubOutput is a subprocess-free sdk.OutputConnector that records its lifecycle.
type stubOutput struct {
	mu        sync.Mutex
	openErr   error
	opened    int
	closed    int
	delivered int
}

func (s *stubOutput) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "stub", Type: sdk.TypeOutput}
}

func (s *stubOutput) Open(context.Context, sdk.Config) error {
	s.mu.Lock()
	s.opened++
	err := s.openErr
	s.mu.Unlock()
	return err
}

func (s *stubOutput) Notify(context.Context, sdk.Notification) error {
	s.mu.Lock()
	s.delivered++
	s.mu.Unlock()
	return nil
}

func (s *stubOutput) Close(context.Context) error {
	s.mu.Lock()
	s.closed++
	s.mu.Unlock()
	return nil
}

func (s *stubOutput) counts() (opened, closed, delivered int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opened, s.closed, s.delivered
}

// recordingDispenser is the injected verified-dispense seam: it hands back a fresh
// stubOutput per call (recorded in order) with a NIL go-plugin client, so no process
// is launched and TrackOutputPlugin/UntrackOutputPlugin exercise their nil-client
// path. dispenseErr / nextOpenErr let a test force a failed dispense or Open.
type recordingDispenser struct {
	mu          sync.Mutex
	dispensed   []*stubOutput
	dispenseErr error
	nextOpenErr error
}

func (rd *recordingDispenser) fn(_ string, _ string) (sdk.OutputConnector, *goplugin.Client, error) {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	if rd.dispenseErr != nil {
		return nil, nil, rd.dispenseErr
	}
	s := &stubOutput{openErr: rd.nextOpenErr}
	rd.nextOpenErr = nil
	rd.dispensed = append(rd.dispensed, s)
	return s, nil, nil
}

func (rd *recordingDispenser) at(i int) *stubOutput {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	if i < 0 || i >= len(rd.dispensed) {
		return nil
	}
	return rd.dispensed[i]
}

func (rd *recordingDispenser) count() int {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	return len(rd.dispensed)
}

// newExtDispatcher builds a dispatcher wired to a real (non-started) runtime and the
// injected dispense seam, with the given trust root.
func newExtDispatcher(trust *connectorTrustSpec) (*connectorDispatcher, *recordingDispenser) {
	rd := &recordingDispenser{}
	d := newConnectorDispatcher(nil, trust, quietLog())
	d.rt = runtime.New(runtime.Options{Logger: quietLog()})
	d.dispenseVerified = rd.fn
	return d, rd
}

// combinedTrust merges the bare-key trust anchors of several fixtures into the ONE
// dispatcher trust root (external destinations share the single ConnectorTrust).
func combinedTrust(fx ...extFixture) *connectorTrustSpec {
	var keys []string
	for _, f := range fx {
		keys = append(keys, f.trust.TrustedKeys...)
	}
	return &connectorTrustSpec{TrustedKeys: keys}
}

func extSpec(name string, fx extFixture) notifyDestinationSpec {
	return notifyDestinationSpec{Name: name, Plugin: &fx.spec}
}

// TestExternalOutputAdmittedAndDelivers: a signed, pinned external output binary is
// admitted, opened and delivered to; admission runs BEFORE any dispense.
func TestExternalOutputAdmittedAndDelivers(t *testing.T) {
	fx := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	d, rd := newExtDispatcher(&fx.trust)
	if !d.openExternal(context.Background(), extSpec("sink", fx), nil, quietLog()) {
		t.Fatal("a trusted, pinned, signed external output must be wired")
	}
	if got := d.Destinations(); len(got) != 1 || got[0] != "sink" {
		t.Fatalf("Destinations() = %v, want [sink]", got)
	}
	if err := d.Deliver(context.Background(), model.NewTenantID(), "sink", sdk.Notification{Title: "hi"}); err != nil {
		t.Fatalf("Deliver to external output: %v", err)
	}
	if opened, _, delivered := rd.at(0).counts(); opened != 1 || delivered != 1 {
		t.Fatalf("stub opened=%d delivered=%d, want 1/1", opened, delivered)
	}
}

// TestExternalOutputDigestMismatchNotWired: a binary edited AFTER signing fails the
// digest pin, so it is refused and never dispensed (admission precedes dispense).
func TestExternalOutputDigestMismatchNotWired(t *testing.T) {
	fx := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	if err := os.WriteFile(fx.bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d, rd := newExtDispatcher(&fx.trust)
	if d.openExternal(context.Background(), extSpec("sink", fx), nil, quietLog()) {
		t.Fatal("a tampered external output must NOT be wired")
	}
	if rd.count() != 0 {
		t.Fatalf("a refused destination must never dispense (dispensed=%d)", rd.count())
	}
	if err := d.Deliver(context.Background(), model.NewTenantID(), "sink", sdk.Notification{}); !errors.Is(err, notify.ErrUnknownDestination) {
		t.Fatalf("Deliver to a refused destination = %v, want ErrUnknownDestination", err)
	}
}

// TestExternalOutputDenyClosedWithoutTrust: with no trust root, every external output
// deny-closes (there is no observe mode, no second trust path).
func TestExternalOutputDenyClosedWithoutTrust(t *testing.T) {
	fx := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	d, rd := newExtDispatcher(nil)
	if d.openExternal(context.Background(), extSpec("sink", fx), nil, quietLog()) {
		t.Fatal("nil trust root must deny-close every external output")
	}
	if rd.count() != 0 {
		t.Fatal("a deny-closed destination must never dispense")
	}
}

// TestExternalOutputOpenFailureNotWired: a dispense that opens with an error is torn
// down and never wired (the kill-on-failed-open contract; readiness is Open-gated).
func TestExternalOutputOpenFailureNotWired(t *testing.T) {
	fx := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	d, rd := newExtDispatcher(&fx.trust)
	rd.nextOpenErr = errors.New("open failed")
	if d.openExternal(context.Background(), extSpec("sink", fx), nil, quietLog()) {
		t.Fatal("a destination that fails Open must NOT be wired")
	}
	if got := d.Destinations(); len(got) != 0 {
		t.Fatalf("Destinations() = %v, want [] (Open failed)", got)
	}
	if opened, _, _ := rd.at(0).counts(); opened != 1 {
		t.Fatal("Open must have been attempted once before the destination was rejected")
	}
}

// TestExternalOutputAtomicReload: reloadExternal prepares+opens the NEW build fully,
// swaps it in atomically, and tears the OLD subprocess down — deliveries after the
// reload reach the new connector, the old is Closed and receives nothing more.
func TestExternalOutputAtomicReload(t *testing.T) {
	fxA := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	fxB := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	d, rd := newExtDispatcher(combinedTrust(fxA, fxB))

	if !d.openExternal(context.Background(), extSpec("sink", fxA), nil, quietLog()) {
		t.Fatal("initial external output must wire")
	}
	if err := d.reloadExternal(context.Background(), extSpec("sink", fxB), nil, quietLog()); err != nil {
		t.Fatalf("atomic reload of a trusted new build must succeed: %v", err)
	}
	if err := d.Deliver(context.Background(), model.NewTenantID(), "sink", sdk.Notification{Title: "after"}); err != nil {
		t.Fatalf("Deliver after reload: %v", err)
	}
	// Old (index 0): opened, then Closed by the reload teardown, no post-swap delivery.
	if opened, closed, delivered := rd.at(0).counts(); opened != 1 || closed != 1 || delivered != 0 {
		t.Fatalf("old connector opened=%d closed=%d delivered=%d, want 1/1/0", opened, closed, delivered)
	}
	// New (index 1): opened before the swap, receives the post-reload delivery, live.
	if opened, closed, delivered := rd.at(1).counts(); opened != 1 || closed != 0 || delivered != 1 {
		t.Fatalf("new connector opened=%d closed=%d delivered=%d, want 1/0/1", opened, closed, delivered)
	}
}

// TestExternalOutputRefusedReloadKeepsOldLive: a reload whose new build fails
// admission is atomic — the previously admitted connector stays live and delivering,
// never a half-open sink.
func TestExternalOutputRefusedReloadKeepsOldLive(t *testing.T) {
	fxA := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	fxB := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	d, rd := newExtDispatcher(combinedTrust(fxA, fxB))

	if !d.openExternal(context.Background(), extSpec("sink", fxA), nil, quietLog()) {
		t.Fatal("initial external output must wire")
	}
	// Tamper the new build so it fails the digest pin at reload.
	if err := os.WriteFile(fxB.bin, []byte("#!/bin/sh\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.reloadExternal(context.Background(), extSpec("sink", fxB), nil, quietLog()); err == nil {
		t.Fatal("a reload whose new build fails admission must return an error")
	}
	// The old connector is untouched and still delivering.
	if err := d.Deliver(context.Background(), model.NewTenantID(), "sink", sdk.Notification{Title: "still-here"}); err != nil {
		t.Fatalf("Deliver after a refused reload: %v", err)
	}
	if opened, closed, delivered := rd.at(0).counts(); opened != 1 || closed != 0 || delivered != 1 {
		t.Fatalf("old connector opened=%d closed=%d delivered=%d, want 1/0/1 (must stay live)", opened, closed, delivered)
	}
}

// TestExternalOutputReconcile exercises the SIGHUP reconcile: add a new external
// destination, leave an unchanged one alone, reload one whose config changed, and
// remove one dropped from the config.
func TestExternalOutputReconcile(t *testing.T) {
	fxKeep := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	fxMod := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	fxAdd := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	trust := combinedTrust(fxKeep, fxMod, fxAdd)
	d, _ := newExtDispatcher(trust)

	// Boot two external destinations.
	keep := extSpec("keep", fxKeep)
	mod := extSpec("mod", fxMod)
	if !d.openExternal(context.Background(), keep, nil, quietLog()) || !d.openExternal(context.Background(), mod, nil, quietLog()) {
		t.Fatal("both initial external destinations must wire")
	}

	// Reconcile: keep unchanged, mod's config changed (→ reload), add a new one.
	modChanged := extSpec("mod", fxMod)
	modChanged.Config = map[string]string{"rate": "slow"} // fingerprint changes → reload
	add := extSpec("add", fxAdd)
	rep := d.reconcileExternal(context.Background(), []notifyDestinationSpec{keep, modChanged, add}, trust, nil, quietLog())
	if rep.Unchanged != 1 || rep.Reloaded != 1 || rep.Added != 1 || rep.Removed != 0 || rep.Refused != 0 {
		t.Fatalf("reconcile report = %+v, want unchanged=1 reloaded=1 added=1 removed=0 refused=0", rep)
	}
	if got := d.Destinations(); len(got) != 3 {
		t.Fatalf("after add, Destinations() = %v, want 3", got)
	}

	// Reconcile down to just "keep": mod and add are removed.
	rep = d.reconcileExternal(context.Background(), []notifyDestinationSpec{keep}, trust, nil, quietLog())
	if rep.Removed != 2 || rep.Unchanged != 1 {
		t.Fatalf("reconcile report = %+v, want removed=2 unchanged=1", rep)
	}
	if got := d.Destinations(); len(got) != 1 || got[0] != "keep" {
		t.Fatalf("after removals, Destinations() = %v, want [keep]", got)
	}
}

// TestExternalOutputTrustRevocation: removing the signing anchor of a LIVE external
// destination and reconciling (SIGHUP) tears it down deny-closed — the running binary,
// signed by the now-untrusted key, is revoked, not left alive until restart.
func TestExternalOutputTrustRevocation(t *testing.T) {
	fx := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	other := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1) // unrelated trust anchor
	d, rd := newExtDispatcher(&fx.trust)
	spec := extSpec("sink", fx)
	if !d.openExternal(context.Background(), spec, nil, quietLog()) {
		t.Fatal("initial external output must wire")
	}
	// Operator rotates the trust root to one that does NOT include fx's key and SIGHUPs.
	rep := d.reconcileExternal(context.Background(), []notifyDestinationSpec{spec}, &other.trust, nil, quietLog())
	if rep.Revoked != 1 {
		t.Fatalf("reconcile report = %+v, want Revoked=1 (the revoked live destination)", rep)
	}
	if got := d.Destinations(); len(got) != 0 {
		t.Fatalf("a revoked destination must be torn down, Destinations() = %v", got)
	}
	if _, closed, _ := rd.at(0).counts(); closed != 1 {
		t.Fatal("the revoked connector must be Closed")
	}
	if err := d.Deliver(context.Background(), model.NewTenantID(), "sink", sdk.Notification{}); !errors.Is(err, notify.ErrUnknownDestination) {
		t.Fatalf("Deliver to a revoked destination = %v, want ErrUnknownDestination", err)
	}
}

// TestExternalOutputTrustUnchangedNoRevocation: an identical trust root across a
// reconcile does NOT churn a live destination (no needless revocation re-admission).
func TestExternalOutputTrustUnchangedNoRevocation(t *testing.T) {
	fx := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	d, _ := newExtDispatcher(&fx.trust)
	spec := extSpec("sink", fx)
	if !d.openExternal(context.Background(), spec, nil, quietLog()) {
		t.Fatal("initial external output must wire")
	}
	rep := d.reconcileExternal(context.Background(), []notifyDestinationSpec{spec}, &fx.trust, nil, quietLog())
	if rep.Revoked != 0 || rep.Unchanged != 1 {
		t.Fatalf("reconcile report = %+v, want Revoked=0 Unchanged=1", rep)
	}
	if got := d.Destinations(); len(got) != 1 || got[0] != "sink" {
		t.Fatalf("an unchanged destination must stay live, Destinations() = %v", got)
	}
}

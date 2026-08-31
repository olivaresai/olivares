// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package tak

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// source_test.go covers the connector's public lifecycle: Descriptor stability, the
// deny-closed Open validations, and the three Gather shapes — honest no-op, posture
// only, and a listener whose shutdown must still flush its aggregate rejections.

// recSink is a mutex-guarded sink (the -race gate requires it: listeners emit from a
// goroutine the test also reads).
type recSink struct {
	mu  sync.Mutex
	obs []model.Observation
}

func (s *recSink) Emit(_ context.Context, o model.Observation) error {
	s.mu.Lock()
	s.obs = append(s.obs, o)
	s.mu.Unlock()
	return nil
}

func (s *recSink) snapshot() []model.Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.Observation, len(s.obs))
	copy(out, s.obs)
	return out
}

func (s *recSink) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.obs)
}

func onlyFindings(obs []model.Observation) []model.FindingReport {
	var out []model.FindingReport
	for _, o := range obs {
		if f, ok := o.(model.FindingReport); ok {
			out = append(out, f)
		}
	}
	return out
}

func TestDescriptorIsStable(t *testing.T) {
	d := New().Descriptor()
	if d.Name != "olivares.tak" {
		t.Errorf("Name = %q, want olivares.tak", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want %q", d.Type, sdk.TypeSource)
	}
	if d.APIVersion != sdk.APIVersion {
		t.Errorf("APIVersion = %q, want %q", d.APIVersion, sdk.APIVersion)
	}
	seen := map[string]bool{}
	for _, f := range d.ConfigFields {
		if f.Key == "" {
			t.Error("ConfigField with empty Key")
		}
		if seen[f.Key] {
			t.Errorf("duplicate ConfigField key %q", f.Key)
		}
		seen[f.Key] = true
	}
}

func TestOpenRejectsPlaintextServerURL(t *testing.T) {
	err := New().Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgServerURL: "http://takserver.example.mil:8443",
	}})
	if err == nil {
		t.Fatal("Open accepted a plaintext http:// server_url, want error")
	}
}

func TestOpenRejectsServerURLWithoutClientCert(t *testing.T) {
	err := New().Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgServerURL: "https://takserver.example.mil:8443",
	}})
	if !errors.Is(err, ErrPostureUnauthenticated) {
		t.Fatalf("Open error = %v, want ErrPostureUnauthenticated", err)
	}
}

func TestOpenRejectsBadUIDMode(t *testing.T) {
	err := New().Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgCoTUIDMode: "plaintext-please",
	}})
	if err == nil {
		t.Fatal("Open accepted an unknown cot_uid_mode, want error")
	}
}

func TestOpenRejectsBadMulticast(t *testing.T) {
	err := New().Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgCoTUDPListen: "0.0.0.0:6969",
		cfgCoTMulticast: "10.0.0.1", // a unicast address
	}})
	if err == nil {
		t.Fatal("Open accepted a non-multicast group address, want error")
	}
}

func TestOpenRejectsMulticastWithoutUDP(t *testing.T) {
	err := New().Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgCoTMulticast: "239.2.3.1", // valid group, but no UDP listener to join it on
	}})
	if err == nil {
		t.Fatal("Open accepted a multicast group with no UDP listener, want error")
	}
}

// TestGatherNoConfigIsHonestNoOp guards the failure mode the connector exists to
// avoid: with no posture source and no listener it must emit NOTHING, never a
// fabricated clean posture.
func TestGatherNoConfigIsHonestNoOp(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &recSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather returned %v, want nil for an unconfigured connector", err)
	}
	if n := sink.len(); n != 0 {
		t.Fatalf("Gather emitted %d observations, want 0 (no fabricated posture)", n)
	}
}

// TestGatherPostureOnlyEmitsAndReturns: a CoreConfig on disk, no listeners. Gather
// must emit the posture findings and RETURN (it must not block waiting on ingest).
func TestGatherPostureOnlyEmitsAndReturns(t *testing.T) {
	path := writeCoreConfig(t, `<Configuration><security><tls keystorePass="atakatak"/></security></Configuration>`)
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgCoreConfigPath: path,
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	sink := &recSink{}
	done := make(chan error, 1)
	go func() { done <- s.Gather(context.Background(), sink) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Gather returned %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("posture-only Gather blocked; it must return once posture is emitted")
	}

	findings := onlyFindings(sink.snapshot())
	if len(findings) == 0 {
		t.Fatal("posture-only Gather emitted no findings")
	}
	if !hasFindingKind(findings, findingDefaultKeystorePass) {
		t.Errorf("want the default-keystore-password finding; got %+v", findings)
	}
	if len(findings) != sink.len() {
		t.Errorf("posture-only Gather emitted non-finding observations: %d findings of %d obs", len(findings), sink.len())
	}
}

// TestServeCoTEmitsEdgeForValidEvent exercises the accepted-event path end to end
// through Gather: a valid CoT datagram must cross the bounded queue and surface at
// the sink as a minimal-data write EdgeObservation on the configured feed. This is
// the counterpart to the reject-flush test — the happy path the drain must not drop.
func TestServeCoTEmitsEdgeForValidEvent(t *testing.T) {
	addr := freeUDPAddr(t)
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgCoTUDPListen: addr,
		cfgFeedRef:      "live-feed",
		cfgPosture:      "false",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	sink := &recSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	stop := pumpUDP(t, addr, validRaw())
	defer stop()

	firstEdge := func() (model.EdgeObservation, bool) {
		for _, o := range sink.snapshot() {
			if e, ok := o.(model.EdgeObservation); ok {
				return e, true
			}
		}
		return model.EdgeObservation{}, false
	}

	if !waitUntil(3*time.Second, func() bool { _, ok := firstEdge(); return ok }) {
		t.Fatal("no EdgeObservation reached the sink for a valid CoT event")
	}
	edge, _ := firstEdge()

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Gather returned %v, want nil or context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Gather did not return after cancel")
	}

	if edge.ResourceRef != "live-feed" {
		t.Errorf("edge.ResourceRef = %q, want live-feed (the feed_ref)", edge.ResourceRef)
	}
	if edge.Mode != model.ModeWrite {
		t.Errorf("edge.Mode = %q, want write", edge.Mode)
	}
	if edge.Source != model.SignalCoT {
		t.Errorf("edge.Source = %q, want %q", edge.Source, model.SignalCoT)
	}
	if edge.ToolRef != transportUDP {
		t.Errorf("edge.ToolRef = %q, want %q", edge.ToolRef, transportUDP)
	}
	if edge.Confidence != model.ConfidenceApproximate {
		t.Errorf("edge.Confidence = %q, want approximate (base CoT is unauthenticated)", edge.Confidence)
	}
}

// TestServeCoTFlushesRejectsOnShutdown drives a real listener into a rejection, then
// cancels the context and asserts the aggregate rejection finding still reached the
// sink via the final flush — a short-lived flood must not leave the ledger empty.
func TestServeCoTFlushesRejectsOnShutdown(t *testing.T) {
	addr := freeUDPAddr(t)
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgCoTUDPListen: addr,
		cfgPosture:      "false",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	sink := &recSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	// Send malformed traffic; it is counted (not emitted per-packet) into s.rejects.
	stop := pumpUDP(t, addr, "not-a-cot-event")
	defer stop()

	// Synchronize on the counter itself (shared with the listener under s.mu), so we
	// cancel only AFTER a rejection has been registered — no dependence on wall time.
	registered := waitUntil(3*time.Second, func() bool {
		s.mu.Lock()
		n := len(s.rejects)
		s.mu.Unlock()
		return n > 0
	})
	if !registered {
		t.Fatal("no rejection was counted before shutdown")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Gather returned %v, want nil or context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Gather did not return after cancel")
	}

	findings := onlyFindings(sink.snapshot())
	if !hasFindingKind(findings, findingCoTRejected) {
		t.Fatalf("no aggregate rejection finding reached the sink on shutdown; got %d obs", sink.len())
	}
}

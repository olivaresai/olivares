// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"

	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/eventbus/natsbus"
	"github.com/olivaresai/olivares/modules/voice"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

// startNATS runs an embedded nats-server on an ephemeral loopback port — the
// real server, in-process, so the gate needs no external infrastructure and
// the bridge is tested against genuine NATS semantics (NoEcho included).
func startNATS(t *testing.T) *natsserver.Server {
	return startNATSOnPort(t, -1)
}

func startNATSOnPort(t *testing.T, port int) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: port, NoLog: true, NoSigs: true,
	})
	if err != nil {
		t.Fatalf("new nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded nats-server did not become ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv
}

func bridgeNode(t *testing.T, url, name, prefix string, buffer int) *natsbus.Bus {
	t.Helper()
	b, err := natsbus.New(natsbus.Config{
		Backend: "nats", URL: url, Name: name, SubjectPrefix: prefix, Buffer: buffer,
	}, natsbus.Options{Decoders: busPayloadDecoders()})
	if err != nil {
		t.Fatalf("bus %s: %v", name, err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// twoNodes builds two bridged buses against one server — the two-HA-nodes
// shape every test here exercises.
func twoNodes(t *testing.T, prefix string) (*natsbus.Bus, *natsbus.Bus) {
	t.Helper()
	srv := startNATS(t)
	return bridgeNode(t, srv.ClientURL(), "node-a", prefix, 0),
		bridgeNode(t, srv.ClientURL(), "node-b", prefix, 0)
}

func edgeEventFor(tenant, ref string) event.Event {
	return event.FromObservation(tenant, "connector:pg", model.EdgeObservation{
		OriginKind: "agent", OriginRef: "a1", ResourceKind: "postgres.table", ResourceRef: ref,
		Mode: model.ModeRead, Source: model.SignalOTEL, Confidence: model.ConfidenceAttributed,
		ObservedAt: time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC),
	})
}

func collect(t *testing.T, bus eventbus.Bus, types []event.Type) <-chan event.Event {
	t.Helper()
	ch := make(chan event.Event, 64)
	sub, err := bus.Subscribe(types, func(_ context.Context, e event.Event) error {
		ch <- e
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Unsubscribe)
	return ch
}

func recvOne(t *testing.T, ch <-chan event.Event, what string) event.Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	// 15s, not 5s: these tests assert DELIVERY, not latency — under a loaded shared
	// box (parallel -race suites) a 5s window flaked (measured 2026-07-24) while the
	// bridge was healthy. A generous window only lengthens the failure path.
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return event.Event{}
	}
}

func assertQuiet(t *testing.T, ch <-chan event.Event, window time.Duration, what string) {
	t.Helper()
	select {
	case e := <-ch:
		t.Fatalf("unexpected delivery (%s): %+v", what, e)
	case <-time.After(window):
	}
}

func waitUntil(t *testing.T, timeout time.Duration, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func recvEventID(t *testing.T, ch <-chan event.Event, id string) event.Event {
	t.Helper()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case e := <-ch:
			if e.ID == id {
				return e
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for event %q", id)
			return event.Event{}
		}
	}
}

// publishUntilReceived republishes e until a copy arrives on ch and returns that
// delivery. The bridge is at-most-once and the remote NATS subscription interest
// propagates ASYNCHRONOUSLY, so a single-shot Publish races the subscription and
// is a lost-delivery flake by construction (observed 2026-07-24 in
// TestBridgeModulePayloadDecoders under a loaded box) — every cross-node
// delivery assertion in this file must go through this retry idiom.
func publishUntilReceived(t *testing.T, bus eventbus.Bus, ch <-chan event.Event, e event.Event) event.Event {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		if err := bus.Publish(context.Background(), e); err != nil {
			t.Fatalf("publish %q: %v", e.ID, err)
		}
		select {
		case got := <-ch:
			if got.ID == e.ID {
				return got
			}
		case <-ticker.C:
			continue
		case <-deadline.C:
			t.Fatalf("timed out establishing cross-node delivery for event %q", e.ID)
		}
	}
}

func drainUntilQuiet(ch <-chan event.Event, quietFor time.Duration) {
	timer := time.NewTimer(quietFor)
	defer timer.Stop()
	for {
		select {
		case <-ch:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(quietFor)
		case <-timer.C:
			return
		}
	}
}

// TestBridgeCrossNodeTypedDelivery is the mechanism end-to-end: an event
// published on node A reaches node B's subscriber over the bridge with its
// payload RE-MATERIALIZED (event.EdgeOf answers — not map[string]any), and
// node A's own subscriber sees it exactly ONCE (local fan-out; NoEcho keeps
// the bridge from delivering A's publish back to A).
func TestBridgeCrossNodeTypedDelivery(t *testing.T) {
	busA, busB := twoNodes(t, "olivares.test.bus1")
	chA := collect(t, busA, []event.Type{event.TypeEdgeObserved})
	chB := collect(t, busB, []event.Type{event.TypeEdgeObserved})

	e := edgeEventFor("tn-1", "public.t")
	e.ID = "e-1"
	if err := busA.Publish(context.Background(), e); err != nil {
		t.Fatal(err)
	}

	got := recvOne(t, chB, "cross-node delivery on B")
	if got.ID != "e-1" || got.Tenant != "tn-1" {
		t.Fatalf("envelope mangled across the bridge: %+v", got)
	}
	edge, ok := event.EdgeOf(got)
	if !ok || edge.ResourceRef != "public.t" {
		t.Fatalf("typed payload must re-materialize on B: ok=%v %T", ok, got.Payload)
	}

	local := recvOne(t, chA, "local delivery on A")
	if local.ID != "e-1" {
		t.Fatalf("local delivery mangled: %+v", local)
	}
	// NoEcho: A must NOT receive its own publish a second time via the bridge.
	assertQuiet(t, chA, 300*time.Millisecond, "echo of A's own publish back to A")
}

// TestBridgeInjectGate: a node whose gate reports standby injects NOTHING from
// the bridge (no remote side effects on a passive node); flipping to leader
// starts injecting. Local publishes are never gated.
func TestBridgeInjectGate(t *testing.T) {
	busA, busB := twoNodes(t, "olivares.test.bus2")
	var leader atomic.Bool // read on the NATS dispatcher goroutine; written below
	busB.SetInjectGate(leader.Load)
	chB := collect(t, busB, nil)

	if err := busA.Publish(context.Background(), edgeEventFor("tn-1", "r1")); err != nil {
		t.Fatal(err)
	}
	assertQuiet(t, chB, 400*time.Millisecond, "remote event on a gated standby")

	// A standby's LOCAL publishes still fan out locally (and bridge outward).
	if err := busB.Publish(context.Background(), edgeEventFor("tn-1", "local")); err != nil {
		t.Fatal(err)
	}
	if got := recvOne(t, chB, "standby local delivery"); got.Payload == nil {
		t.Fatalf("local delivery on the standby must carry its payload")
	}

	leader.Store(true) // promotion
	if err := busA.Publish(context.Background(), edgeEventFor("tn-1", "r2")); err != nil {
		t.Fatal(err)
	}
	got := recvOne(t, chB, "remote event after promotion")
	if edge, ok := event.EdgeOf(got); !ok || edge.ResourceRef != "r2" {
		t.Fatalf("post-promotion event mangled: %+v", got)
	}
}

// TestBridgePerPublisherOrdering: one node's publishes arrive on the peer in
// publish order across DIFFERENT event types — the property the single
// wildcard subscription (vs per-type subscriptions) exists to preserve.
func TestBridgePerPublisherOrdering(t *testing.T) {
	busA, busB := twoNodes(t, "olivares.test.bus3")
	chB := collect(t, busB, nil) // all types, one subscriber queue

	// Preflight (not a bare first Publish): collect() subscribes LOCALLY, but the
	// peer's subscription interest propagates over NATS asynchronously, so under
	// load the first publish can be dropped by the at-most-once bridge before that
	// interest lands. The drop then surfaces as a bogus ORDERING failure — the
	// observed "cross-type order broken at 0: got ord-001 want ord-000" is the
	// stream missing ord-000, not delivering it late. Same race the payload-decoder
	// test documents; same remedy: prove the path is live, then drain the proof.
	publishUntilReceived(t, busA, chB, edgeEventFor("tn-1", "ordering-preflight"))
	drainUntilQuiet(chB, 100*time.Millisecond)

	const n = 40
	for i := 0; i < n; i++ {
		var e event.Event
		if i%2 == 0 {
			e = edgeEventFor("tn-1", fmt.Sprintf("r%03d", i))
		} else {
			e = event.FromObservation("tn-1", "module:finops", model.CostSample{
				ProviderRef: "anthropic", ModelRef: "m", CostMicroUSD: int64(i),
			})
		}
		e.ID = fmt.Sprintf("ord-%03d", i)
		if err := busA.Publish(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		got := recvOne(t, chB, fmt.Sprintf("ordered event %d", i))
		if want := fmt.Sprintf("ord-%03d", i); got.ID != want {
			t.Fatalf("cross-type order broken at %d: got %s want %s", i, got.ID, want)
		}
	}
}

// TestBridgeModulePayloadDecoders: the composition root's decoder registry
// re-materializes module-owned payloads (voice.Telemetry) across nodes — the
// liveingest voice probe publishing on a standby reaches the leader's voice
// module with a payload its tolerant parser accepts as a concrete struct.
func TestBridgeModulePayloadDecoders(t *testing.T) {
	busA, busB := twoNodes(t, "olivares.test.bus4")
	chB := collect(t, busB, []event.Type{voice.TypeVoiceTelemetry})

	// Retry-publish (not single-shot): the remote subscription interest is async,
	// so one bare Publish over the at-most-once bridge can be dropped before the
	// subscription lands — the exact flake this test exhibited under load.
	got := publishUntilReceived(t, busA, chB, event.Event{
		ID: "v-1", Type: voice.TypeVoiceTelemetry, Tenant: "tn-1", Source: "module:liveingest",
		Payload: voice.Telemetry{SessionRef: "s1", AgentRef: "a1", TurnDelta: 3},
	})
	tel, ok := got.Payload.(voice.Telemetry)
	if !ok || tel.TurnDelta != 3 || tel.SessionRef != "s1" {
		t.Fatalf("voice.Telemetry must re-materialize via the boot decoder registry: %T %+v", got.Payload, got.Payload)
	}
}

// TestBridgeFailureDegradation exercises the two shutdown/outage edges where
// the at-most-once bridge must degrade explicitly without wedging the process.
func TestBridgeFailureDegradation(t *testing.T) {
	t.Run("reconnect-resume", func(t *testing.T) {
		srv := startNATS(t)
		port := srv.Addr().(*net.TCPAddr).Port
		prefix := "olivares.test.bus.reconnect"
		busA := bridgeNode(t, srv.ClientURL(), "node-a", prefix, 0)
		busB := bridgeNode(t, srv.ClientURL(), "node-b", prefix, 0)
		chB := collect(t, busB, []event.Type{event.TypeEdgeObserved})

		preflight := edgeEventFor("tn-1", "before-outage")
		preflight.ID = "before-outage"
		publishUntilReceived(t, busA, chB, preflight)
		drainUntilQuiet(chB, 100*time.Millisecond)

		srv.Shutdown()
		srv.WaitForShutdown()
		waitUntil(t, 10*time.Second, "node A to observe the NATS outage", func() bool {
			return !busA.Bridge().Connected
		})
		waitUntil(t, 10*time.Second, "node B to observe the NATS outage", func() bool {
			return !busB.Bridge().Connected
		})

		buffered := edgeEventFor("tn-1", "during-outage")
		buffered.ID = "during-outage"
		if err := busA.Publish(context.Background(), buffered); err != nil {
			t.Fatal(err)
		}
		assertQuiet(t, chB, 400*time.Millisecond, "cross-node delivery during the NATS outage")

		_ = startNATSOnPort(t, port)
		waitUntil(t, 15*time.Second, "node A to reconnect to NATS", func() bool {
			return busA.Bridge().Connected
		})
		waitUntil(t, 15*time.Second, "node B to reconnect to NATS", func() bool {
			return busB.Bridge().Connected
		})

		resumed := edgeEventFor("tn-1", "after-reconnect")
		resumed.ID = "after-reconnect"
		// Retry-publish: Connected=true only says the CLIENT reconnected — the
		// subscription re-establishment on the restarted server is still async,
		// so a single-shot Publish here has the same lost-delivery flake mode.
		got := publishUntilReceived(t, busA, chB, resumed)
		if edge, ok := event.EdgeOf(got); !ok || edge.ResourceRef != "after-reconnect" {
			t.Fatalf("post-reconnect event mangled: %+v", got)
		}
	})

	t.Run("close-no-hang", func(t *testing.T) {
		srv := startNATS(t)
		prefix := "olivares.test.bus.close"
		busA := bridgeNode(t, srv.ClientURL(), "node-a", prefix, 256)
		busB := bridgeNode(t, srv.ClientURL(), "node-b", prefix, 256)
		warmupCh := make(chan event.Event, 8)
		warmupSub, err := busB.Subscribe([]event.Type{event.TypeEdgeObserved}, func(_ context.Context, e event.Event) error {
			warmupCh <- e
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		warmup := edgeEventFor("tn-1", "warmup")
		warmup.ID = "warmup"
		publishUntilReceived(t, busA, warmupCh, warmup)
		drainUntilQuiet(warmupCh, 100*time.Millisecond)
		warmupSub.Unsubscribe()

		handlerEntered := make(chan struct{})
		releaseHandler := make(chan struct{})
		var enterOnce sync.Once
		var releaseOnce sync.Once
		sub, err := busB.Subscribe([]event.Type{event.TypeEdgeObserved}, func(_ context.Context, _ event.Event) error {
			enterOnce.Do(func() { close(handlerEntered) })
			<-releaseHandler
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(sub.Unsubscribe)
		// Close waits for an in-flight handler by contract. Always release it, even
		// when an earlier assertion fails, so test cleanup cannot hang.
		t.Cleanup(func() { releaseOnce.Do(func() { close(releaseHandler) }) })

		for i := 0; i < 512; i++ {
			e := edgeEventFor("tn-1", fmt.Sprintf("queued-%03d", i))
			e.ID = fmt.Sprintf("queued-%03d", i)
			if err := busA.Publish(context.Background(), e); err != nil {
				t.Fatalf("publish %d: %v", i, err)
			}
		}
		select {
		case <-handlerEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("node B handler never entered")
		}
		waitUntil(t, 5*time.Second, "node B local queue to fill", func() bool {
			stats := busB.BusStats()
			return len(stats.Subscribers) == 1 && stats.Subscribers[0].Depth == 256
		})
		waitUntil(t, 5*time.Second, "bridge callback backpressure", func() bool {
			return busB.Bridge().PendingMsgs > 0
		})

		closeDone := make(chan error, 1)
		go func() { closeDone <- busB.Close() }()
		select {
		case err := <-closeDone:
			t.Fatalf("Close returned before the in-flight handler was released: %v", err)
		case <-time.After(100 * time.Millisecond):
		}

		releaseOnce.Do(func() { close(releaseHandler) })
		select {
		case err := <-closeDone:
			if err != nil {
				t.Fatalf("Close: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Close hung after the handler was released")
		}
	})
}

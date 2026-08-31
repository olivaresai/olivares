// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"

	"github.com/olivaresai/olivares/core/eventbus/natsbus"
)

// A bridge that has returned from construction is one the server can already route to.
//
// nc.Subscribe only queues the SUB protocol message on the client's outbound buffer. Core NATS
// has no store-and-forward, so anything published into the gap between that queueing and the
// server registering the interest is dropped in silence — by the server, which has nobody to
// route it to. No deadline on the receiving side can recover it: by the time the wait begins the
// message no longer exists.
//
// The gap was not narrow. Measured before the fix, on this same embedded server: 595 of 600
// constructions returned before the subscription existed server-side. It stayed invisible
// because a node normally does other work before anyone publishes to it.
// TestBridgeCrossNodeTypedDelivery is the case that does not.
//
// The assertion is EXACT growth, not "the number changed": a count that moved for an unrelated
// reason would satisfy the weaker form, and this file exists precisely because a weaker check
// let a real gap through.
func TestBridgeSubscriptionIsLiveWhenNewReturns(t *testing.T) {
	const rounds = 25
	srv := startNATS(t)
	for i := 0; i < rounds; i++ {
		before := srv.NumSubscriptions()
		b := bridgeNode(t, srv.ClientURL(), "subready", "olivares.test.subready", 0)
		if got, want := srv.NumSubscriptions(), before+1; got != want {
			t.Fatalf("round %d: server holds %d subscriptions after New returned, want exactly %d — "+
				"the bridge is advertised as started while the server cannot route to it", i, got, want)
		}
		if !b.SubscriptionConfirmed() {
			t.Fatalf("round %d: the bridge reports its subscription unconfirmed while the server holds it", i)
		}
	}
}

// A flush proves the SUB was PROCESSED. It does not prove it was ALLOWED — a server that refuses
// a subject answers out of band, after the flush has already returned nil. A bridge that read a
// nil flush as readiness would report itself started while the server had inserted nothing into
// its routing table: cross-node input silently absent, Connected still true, and every pending,
// drop and decode counter still zero. That is the one fault shape none of this bus's instruments
// can see, so construction refuses it — a refusal does not resolve itself the way an outage does,
// and the two need different answers.
func TestBridgeRefusesToStartWhenTheServerRejectsTheSubscription(t *testing.T) {
	const prefix = "olivares.test.denied"
	srv := startNATSDenyingSubscribe(t, prefix+".>")

	_, err := natsbus.New(natsbus.Config{
		Backend: "nats", URL: withCreds(srv.ClientURL(), "app", "secret"), Name: "denied",
		SubjectPrefix: prefix,
	}, natsbus.Options{Decoders: busPayloadDecoders()})
	if err == nil {
		t.Fatal("New succeeded against a server that refuses the bridge subscription: the bridge " +
			"would report itself started while receiving nothing cross-node")
	}
	if !strings.Contains(err.Error(), "refused the bridge subscription") {
		t.Errorf("error must name what was refused, got: %v", err)
	}
}

// Readiness is not a fact about the transport. It dies with the connection and has to be earned
// again, because the client re-sends its subscriptions on reconnect ASYNCHRONOUSLY and never
// checks whether they were accepted. Announcing "reconnected" as if it meant "routable" is how a
// publish issued right after a reported recovery is lost for good.
func TestBridgeReadinessDiesWithTheConnectionAndIsEarnedBack(t *testing.T) {
	srv := startNATS(t)
	port := srv.Addr().(*net.TCPAddr).Port
	b := bridgeNode(t, srv.ClientURL(), "readiness", "olivares.test.readiness", 0)
	if !b.SubscriptionConfirmed() {
		t.Fatal("a freshly constructed bridge against a live server must be confirmed")
	}

	srv.Shutdown()
	srv.WaitForShutdown()
	waitUntil(t, 10*time.Second, "the bridge to observe the outage", func() bool {
		return !b.Bridge().Connected
	})
	if b.SubscriptionConfirmed() {
		t.Error("readiness survived the connection: a subscription the server no longer holds is " +
			"not confirmed, and reporting it as such for the length of an outage is a false green")
	}

	_ = startNATSOnPort(t, port)
	waitUntil(t, 20*time.Second, "the bridge to reconnect", func() bool {
		return b.Bridge().Connected
	})
	// The point of the whole state: Connected comes back FIRST and routable comes back after.
	// TestBridgeFailureDegradation works around exactly this gap with a retry-publish loop.
	waitUntil(t, 20*time.Second, "the bridge to earn its readiness back", func() bool {
		return b.SubscriptionConfirmed()
	})
	if got := b.Bridge().SubscriptionConfirmed; !got {
		t.Error("the stats surface must expose the confirmed state, not only the transport one")
	}
}

// withCreds injects a username and password into a nats:// URL the embedded server hands out.
func withCreds(url, user, pass string) string {
	return strings.Replace(url, "nats://", "nats://"+user+":"+pass+"@", 1)
}

// startNATSDenyingSubscribe runs an embedded server whose only user may publish anywhere but may
// NOT subscribe to deny — the authorization shape that makes a flush lie.
func startNATSDenyingSubscribe(t *testing.T, deny string) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true,
		Users: []*natsserver.User{{
			Username: "app", Password: "secret",
			Permissions: &natsserver.Permissions{
				Publish:   &natsserver.SubjectPermission{Allow: []string{">"}},
				Subscribe: &natsserver.SubjectPermission{Deny: []string{deny}},
			},
		}},
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

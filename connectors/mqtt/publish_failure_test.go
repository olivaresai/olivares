// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// MQTT 5 section 3.8.4: a PUBLISH may arrive before the SUBACK, and it keeps its RETAIN flag. A
// retained message that arrives early must not look like a live one to the caller.
func TestPublishReaderKeepsTheRetainFlagOfAnEarlyPublication(t *testing.T) {
	b := newFakeBroker(t, connackOK, subackOK, fakePublish{topic: "a", payload: []byte("x"), flags: 1, early: true})
	got, err := publishReader(t, b.addr, PublishOptions{Topic: "a", Timeout: time.Second}).Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Retain || string(got.Payload) != "x" {
		t.Fatalf("the early retained publication came back as %+v", got)
	}
}

// A broker or a network that stalls in the middle of a packet must not leave the reader parsing the
// rest of that packet as a new one. The whole-call deadline is the only read deadline.
func TestPublishReaderToleratesABrokerThatStallsInsideAPacket(t *testing.T) {
	want := `{"t":1}`
	b := newFakeBroker(t, connackOK, subackOK, fakePublish{topic: "a", payload: []byte(want), stall: 1300 * time.Millisecond})
	got, err := publishReader(t, b.addr, PublishOptions{Topic: "a", Timeout: 4 * time.Second}).Read(t.Context())
	if err != nil || string(got.Payload) != want {
		t.Fatalf("a publication sent in two halves came back as %+v, %v", got, err)
	}
}

// A packet that declares more than payload + topic + overhead is refused before its body is read,
// so a hostile declared length costs no allocation and no wait.
func TestPublishReaderRefusesAPacketThatDeclaresMoreThanTheLimit(t *testing.T) {
	b := newFakeBroker(t, connackOK, subackOK, fakePublish{declared: 1 << 20})
	started := time.Now()
	_, err := publishReader(t, b.addr, PublishOptions{Topic: "a", MaxPayload: 16, Timeout: 3 * time.Second}).Read(t.Context())
	if !errors.Is(err, ErrPayloadTooLarge) || time.Since(started) > time.Second {
		t.Fatalf("error = %v after %v, want ErrPayloadTooLarge at once", err, time.Since(started))
	}
	// Just inside the limit: the same declared length as a real packet with a payload at the cap passes.
	ok := newFakeBroker(t, connackOK, subackOK, fakePublish{topic: "a", payload: []byte(strings.Repeat("x", 16))})
	if _, err := publishReader(t, ok.addr, PublishOptions{Topic: "a", MaxPayload: 16, Timeout: time.Second}).Read(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// A connection that ends while the reader waits is not a silent device: it is its own error, and
// TemperatureReader keeps saying "sensor unavailable" for it.
func TestPublishReaderTellsALostConnectionFromASilentBroker(t *testing.T) {
	lost := newFakeBroker(t, connackOK, subackOK, fakePublish{hangUp: true})
	if _, err := publishReader(t, lost.addr, PublishOptions{Topic: "a", Timeout: time.Second}).Read(t.Context()); !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("a hung-up connection: %v, want ErrConnectionLost", err)
	}
	r, err := NewTemperatureReader(sdk.Config{Settings: map[string]string{"broker": lost.addr, "topic": "a", "timeout": "1s"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(t.Context()); err == nil || err.Error() != "mqtt temperature: sensor unavailable" {
		t.Fatalf("TemperatureReader on a lost connection: %v", err)
	}
}

// A packet the reader cannot parse while it waits (here a topic length that runs past the end of the
// packet) is a broker problem, not a silent device and not a publication.
func TestPublishReaderCallsAnUnusablePacketAConnectionProblem(t *testing.T) {
	b := newFakeBroker(t, connackOK, subackOK, fakePublish{rawBody: []byte{0x00, 0x09, 'a'}})
	if _, err := publishReader(t, b.addr, PublishOptions{Topic: "a", Timeout: time.Second}).Read(t.Context()); !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("an unparsable PUBLISH: %v, want ErrConnectionLost", err)
	}
}

// A failed TLS handshake (an untrusted certificate, a broker that does not speak TLS) is told apart
// from an outage so an operator looks at the certificate; TemperatureReader keeps its one sentence.
func TestPublishReaderTellsATLSFailureFromAnOutage(t *testing.T) {
	plain := newFakeBroker(t, connackOK, subackOK)
	tlsURL := strings.Replace(plain.addr, "tcp://", "tls://", 1)
	_, err := publishReader(t, tlsURL, PublishOptions{Topic: "a", Timeout: time.Second}).Read(t.Context())
	if !errors.Is(err, ErrTLSHandshake) {
		t.Fatalf("TLS to a plain broker: %v, want ErrTLSHandshake", err)
	}
	if strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("the error names the broker: %v", err)
	}
	r, err := NewTemperatureReader(sdk.Config{Settings: map[string]string{"broker": tlsURL, "topic": "a", "timeout": "1s"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(t.Context()); err == nil || err.Error() != "mqtt temperature: broker unavailable" {
		t.Fatalf("TemperatureReader on a failed handshake: %v", err)
	}
}

// The caller's context ends the read at once; the reader's own timeout is far longer here, so only
// a reader that honours cancellation passes.
func TestPublishReaderHonoursCallerCancellation(t *testing.T) {
	silent := newFakeBroker(t, connackOK, subackOK)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(150*time.Millisecond, cancel)
	started := time.Now()
	_, err := publishReader(t, silent.addr, PublishOptions{Topic: "a", Timeout: 5 * time.Second}).Read(ctx)
	if !errors.Is(err, ErrNoPublish) || time.Since(started) > time.Second {
		t.Fatalf("error = %v after %v, want ErrNoPublish soon after the cancel", err, time.Since(started))
	}
}

// What each reader says about itself to the broker: a clean session, its own identifier prefix, and
// for TemperatureReader no retain handling, as before this export.
func TestReadersIntroduceThemselvesAsBefore(t *testing.T) {
	fresh := `{"celsius":21.75,"observed_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`
	pb := newFakeBroker(t, connackOK, subackOK, fakePublish{topic: "a", payload: []byte("x")})
	if _, err := publishReader(t, pb.addr, PublishOptions{Topic: "a", Timeout: time.Second}).Read(t.Context()); err != nil {
		t.Fatal(err)
	}
	tb := newFakeBroker(t, connackOK, subackOK, fakePublish{topic: "a", payload: []byte(fresh)})
	tr, err := NewTemperatureReader(sdk.Config{Settings: map[string]string{"broker": tb.addr, "topic": "a", "timeout": "1s"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Read(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		broker  *fakeBroker
		prefix  string
		options byte
	}{{"PublishReader", pb, "olivares-read-", 0x00}, {"TemperatureReader", tb, "olivares-temperature-", 0x00}} {
		conns, subs := test.broker.connections(), test.broker.subscriptions()
		if len(conns) != 1 || conns[0].flags&0x02 == 0 || !strings.HasPrefix(conns[0].clientID, test.prefix) || len(conns[0].clientID) <= len(test.prefix) {
			t.Errorf("%s CONNECT = %+v, want a clean start and an identifier with prefix %q", test.name, conns, test.prefix)
		}
		if len(subs) != 1 || subs[0].options != test.options {
			t.Errorf("%s SUBSCRIBE = %+v, want options %#x", test.name, subs, test.options)
		}
	}
}

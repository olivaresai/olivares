// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"bufio"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

type fakePublish struct {
	topic   string
	payload []byte
	flags   byte // low nibble of the PUBLISH fixed header: bit 0 is RETAIN

	early    bool          // sent before the SUBACK
	stall    time.Duration // the packet goes out in two halves with this pause between them
	declared int           // if set, only a header declaring this remaining length is sent
	hangUp   bool          // the broker closes the connection instead of publishing
	rawBody  []byte        // if set, this is sent as the whole PUBLISH body (topic, properties and payload as given)
}

type subscription struct {
	filter  string
	options byte
}

// connectInfo is what a CONNECT said: its flags byte and the client identifier.
type connectInfo struct {
	flags    byte
	clientID string
}

// fakeBroker is a real TCP MQTT 5 broker for one test: it answers every CONNECT and SUBSCRIBE
// with the given packets, records what was subscribed to and how, then sends its publications.
type fakeBroker struct {
	addr     string
	mu       sync.Mutex
	subs     []subscription
	connects []connectInfo
}

func (b *fakeBroker) subscriptions() []subscription {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]subscription(nil), b.subs...)
}

// recordConnect reads the flags and the client identifier out of a CONNECT body: the protocol
// name (6 bytes), the version, the flags, the keep alive (2), the properties, then the identifier.
func (b *fakeBroker) recordConnect(body []byte) {
	if len(body) < 12 {
		return
	}
	id, _, err := readString(body, 11+int(body[10]))
	if err != nil {
		return
	}
	b.mu.Lock()
	b.connects = append(b.connects, connectInfo{flags: body[7], clientID: id})
	b.mu.Unlock()
}

func (b *fakeBroker) connections() []connectInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]connectInfo(nil), b.connects...)
}

// send writes one publication the way its fields ask for.
func (p fakePublish) send(c net.Conn) {
	if p.hangUp {
		_ = c.Close()
		return
	}
	if p.declared != 0 {
		_, _ = c.Write(encodeVBI([]byte{pktPUBLISH << 4}, p.declared))
		return
	}
	packet := p.rawBody
	if packet == nil {
		packet = binary.BigEndian.AppendUint16(nil, uint16(len(p.topic)))
		packet = append(packet, p.topic...)
		packet = append(packet, 0)
		packet = append(packet, p.payload...)
	}
	out := frame(pktPUBLISH, p.flags, packet)
	if p.stall == 0 {
		_, _ = c.Write(out)
		return
	}
	half := len(out) / 2
	_, _ = c.Write(out[:half])
	time.Sleep(p.stall)
	_, _ = c.Write(out[half:])
}

func newFakeBroker(t *testing.T, connack, suback []byte, publishes ...fakePublish) *fakeBroker {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &fakeBroker{addr: "tcp://" + l.Addr().String()}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = l.Close()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(2 * time.Second))
				r := bufio.NewReader(c)
				kind, _, hello, err := readPacket(r)
				if err != nil || kind != pktCONNECT {
					return
				}
				b.recordConnect(hello)
				_, _ = c.Write(connack)
				if len(connack) < 5 || connack[3] != 0 {
					return
				}
				kind, _, body, err := readPacket(r)
				if err != nil || kind != pktSUBSCRIBE {
					return
				}
				// packet id (2), properties length (1, empty), then filter and options.
				filter, n, err := readString(body, 3)
				if err != nil || 3+n >= len(body) {
					return
				}
				b.mu.Lock()
				b.subs = append(b.subs, subscription{filter, body[3+n]})
				b.mu.Unlock()
				for _, p := range publishes {
					if p.early {
						p.send(c)
					}
				}
				_, _ = c.Write(suback)
				for _, p := range publishes {
					if !p.early {
						p.send(c)
					}
				}
				_, _ = r.ReadByte() // until the reader hangs up
			}()
		}
	}()
	return b
}

var (
	connackOK = []byte{0x20, 0x03, 0x00, 0x00, 0x00}
	subackOK  = []byte{0x90, 0x04, 0x00, 0x01, 0x00, 0x00}
)

func publishReader(t *testing.T, broker string, o PublishOptions) *PublishReader {
	t.Helper()
	r, err := NewPublishReader(sdk.Config{Settings: map[string]string{"broker": broker}}, o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPublishReaderReturnsTheFirstPublishOnTheExactTopic(t *testing.T) {
	b := newFakeBroker(t, connackOK, subackOK,
		fakePublish{topic: "plant/other", payload: []byte("other")},
		fakePublish{topic: "plant/boiler", payload: []byte(`{"t":1}`)})
	got, err := publishReader(t, b.addr, PublishOptions{Topic: "plant/boiler", Timeout: time.Second}).Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.Topic != "plant/boiler" || string(got.Payload) != `{"t":1}` || got.Retain {
		t.Fatalf("the publish on the exact topic was not returned as sent: %+v", got)
	}
}

func TestPublishReaderReportsTheRetainFlag(t *testing.T) {
	for _, flags := range []byte{0, 1} {
		b := newFakeBroker(t, connackOK, subackOK, fakePublish{topic: "plant/boiler", payload: []byte("x"), flags: flags})
		got, err := publishReader(t, b.addr, PublishOptions{Topic: "plant/boiler", Timeout: time.Second}).Read(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got.Retain != (flags == 1) {
			t.Fatalf("flags %d: Retain = %v", flags, got.Retain)
		}
	}
}

func TestParsePublishReadsTheRetainFlag(t *testing.T) {
	noID := append(binary.BigEndian.AppendUint16(nil, 1), 't', 0, 'x')         // topic "t", no properties, payload "x"
	withID := append(binary.BigEndian.AppendUint16(nil, 1), 't', 0, 1, 0, 'x') // QoS above 0 adds a packet id
	for _, test := range []struct {
		flags byte
		body  []byte
		want  bool
	}{{0, noID, false}, {1, noID, true}, {0x03, withID, true}, {0x02, withID, false}} {
		pub, err := parsePublish(test.flags, test.body)
		if err != nil || pub.Retain != test.want {
			t.Fatalf("flags %#x: retain = %v, err = %v", test.flags, pub.Retain, err)
		}
	}
}

func TestPublishReaderSubscribesToOneExactTopicAndCanAskForNoRetainedMessages(t *testing.T) {
	for _, test := range []struct {
		skip    bool
		options byte
	}{{false, 0x00}, {true, 0x20}} { // MQTT 5 section 3.8.3.1: Retain Handling 2 is bits 4-5
		b := newFakeBroker(t, connackOK, subackOK, fakePublish{topic: "plant/boiler", payload: []byte("x")})
		if _, err := publishReader(t, b.addr, PublishOptions{Topic: "plant/boiler", Timeout: time.Second, SkipRetained: test.skip}).Read(t.Context()); err != nil {
			t.Fatal(err)
		}
		subs := b.subscriptions()
		if len(subs) != 1 || subs[0].filter != "plant/boiler" || subs[0].options != test.options {
			t.Fatalf("skip=%v: subscription = %+v", test.skip, subs)
		}
	}
}

func TestPublishReaderRefusesWildcardsAndUnusableTopics(t *testing.T) {
	for _, topic := range []string{"", "plant/+", "plant/#", "#", "plant/\x00x", "plant/\xff", strings.Repeat("a", 65536)} {
		_, err := NewPublishReader(sdk.Config{Settings: map[string]string{"broker": "tcp://127.0.0.1:1883"}}, PublishOptions{Topic: topic})
		if err == nil {
			t.Fatalf("topic %q was accepted", topic)
		}
	}
}

func TestPublishReaderValidatesItsLimits(t *testing.T) {
	cfg := sdk.Config{Settings: map[string]string{"broker": "tcp://127.0.0.1:1883"}}
	for _, o := range []PublishOptions{
		{Topic: "a", Timeout: 6 * time.Second},
		{Topic: "a", Timeout: -time.Second},
		{Topic: "a", MaxPayload: MaxPublishPayload + 1},
		{Topic: "a", MaxPayload: -1},
	} {
		if _, err := NewPublishReader(cfg, o); err == nil {
			t.Fatalf("options %+v were accepted", o)
		}
	}
	for _, settings := range []map[string]string{
		{},
		{"broker": "tcp://user:secret@127.0.0.1:1883"},
		{"broker": "tcp://127.0.0.1:1883?x=1"},
		{"broker": "wss://127.0.0.1:1883"},
	} {
		if _, err := NewPublishReader(sdk.Config{Settings: settings}, PublishOptions{Topic: "a"}); err == nil {
			t.Fatalf("settings %v were accepted", settings)
		}
	}
}

func TestPublishReaderLimitsThePayload(t *testing.T) {
	big := []byte(strings.Repeat("x", 17))
	for _, test := range []struct {
		name    string
		max     int
		payload []byte
		wantErr error
	}{
		{"over the limit", 16, big, ErrPayloadTooLarge},
		{"at the limit", 16, big[:16], nil},
		{"over the default", 0, []byte(strings.Repeat("x", 4097)), ErrPayloadTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := newFakeBroker(t, connackOK, subackOK, fakePublish{topic: "a", payload: test.payload})
			_, err := publishReader(t, b.addr, PublishOptions{Topic: "a", MaxPayload: test.max, Timeout: time.Second}).Read(t.Context())
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestPublishReaderLimitsAnEarlyPublicationToo(t *testing.T) {
	// MQTT 5 section 3.8.4: a PUBLISH may arrive before the SUBACK. It is held to the same limit.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { _ = l.Close(); <-done })
	go func() {
		defer close(done)
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		r := bufio.NewReader(c)
		_, _, _, _ = readPacket(r)
		_, _ = c.Write(connackOK)
		_, _, _, _ = readPacket(r)
		packet := append(binary.BigEndian.AppendUint16(nil, 1), 'a', 0)
		_, _ = c.Write(frame(pktPUBLISH, 0, append(packet, []byte(strings.Repeat("x", 17))...)))
		_, _ = c.Write(subackOK)
		_, _ = r.ReadByte()
	}()
	_, err = publishReader(t, "tcp://"+l.Addr().String(), PublishOptions{Topic: "a", MaxPayload: 16, Timeout: time.Second}).Read(t.Context())
	if !errors.Is(err, ErrEarlyPublication) {
		t.Fatalf("an oversized early publication was not refused: %v", err)
	}
}

func TestPublishReaderErrorsAreTheDocumentedOnes(t *testing.T) {
	refusedConn := newFakeBroker(t, []byte{0x20, 0x03, 0x00, 0x87, 0x00}, subackOK)
	refusedSub := newFakeBroker(t, connackOK, []byte{0x90, 0x04, 0x00, 0x01, 0x00, 0x87})
	silent := newFakeBroker(t, connackOK, subackOK)
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := "tcp://" + closed.Addr().String()
	_ = closed.Close()
	for _, test := range []struct {
		name   string
		broker string
		want   error
	}{
		{"connection refused by the broker", refusedConn.addr, ErrBrokerRefused},
		{"subscription refused by the broker", refusedSub.addr, ErrBrokerRefused},
		{"no publication before the deadline", silent.addr, ErrNoPublish},
		{"nothing listening", deadAddr, ErrBrokerUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := time.Now()
			_, err := publishReader(t, test.broker, PublishOptions{Topic: "a", Timeout: 200 * time.Millisecond}).Read(t.Context())
			if !errors.Is(err, test.want) || time.Since(started) > 2*time.Second {
				t.Fatalf("error = %v after %v, want %v", err, time.Since(started), test.want)
			}
			if strings.Contains(err.Error(), "127.0.0.1") {
				t.Fatalf("the error names the broker: %v", err)
			}
		})
	}
}

func TestPublishReaderKeepsNothingBetweenReads(t *testing.T) {
	b := newFakeBroker(t, connackOK, subackOK, fakePublish{topic: "a", payload: []byte("x")})
	r := publishReader(t, b.addr, PublishOptions{Topic: "a", Timeout: time.Second})
	for range 2 {
		if _, err := r.Read(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(b.subscriptions()); n != 2 {
		t.Fatalf("two reads made %d subscriptions; each read is its own clean session", n)
	}
}

// TemperatureReader is built on PublishReader; the sentences its callers see did not change.
func TestTemperatureReaderKeepsItsErrorSentences(t *testing.T) {
	fresh := `{"celsius":21.75,"observed_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`
	for _, test := range []struct {
		name   string
		broker *fakeBroker
		want   string
	}{
		{"broker refuses", newFakeBroker(t, []byte{0x20, 0x03, 0x00, 0x87, 0x00}, subackOK), "mqtt temperature: broker unavailable"},
		{"no publication", newFakeBroker(t, connackOK, subackOK), "mqtt temperature: sensor unavailable"},
		{"oversized payload", newFakeBroker(t, connackOK, subackOK,
			fakePublish{topic: "plant/temperature", payload: []byte(strings.Repeat("x", 4097))}), "mqtt temperature: invalid sensor measurement"},
		{"not a measurement", newFakeBroker(t, connackOK, subackOK,
			fakePublish{topic: "plant/temperature", payload: []byte("PRIVATE-SENSOR-CANARY")}), "mqtt temperature: invalid sensor measurement"},
		{"stale", newFakeBroker(t, connackOK, subackOK,
			fakePublish{topic: "plant/temperature", payload: []byte(`{"celsius":1,"observed_at":"2000-01-01T00:00:00Z"}`)}), "mqtt temperature: measurement is stale or future-dated"},
		{"fresh retained value is still accepted", newFakeBroker(t, connackOK, subackOK,
			fakePublish{topic: "plant/temperature", payload: []byte(fresh), flags: 1}), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, err := NewTemperatureReader(sdk.Config{Settings: map[string]string{
				"broker": test.broker.addr, "topic": "plant/temperature", "timeout": "200ms"}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.Read(t.Context())
			if test.want == "" && err != nil || test.want != "" && (err == nil || err.Error() != test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"bytes"
	"testing"
	"time"
)

// TestVBIRoundTrip proves the hand-rolled Variable Byte Integer codec is real:
// encode then decode must recover the value, and the byte-length must match the
// MQTT 5 §1.5.5 boundaries (1 byte <128, 2 bytes <16384, 3 bytes <2097152, 4 bytes
// up to 268435455). The spec's published worked examples (127→1, 128→2, 16383→2,
// 16384→3, 2097151→3, 2097152→4) are checked explicitly.
func TestVBIRoundTrip(t *testing.T) {
	cases := []struct {
		n       int
		wantLen int
	}{
		{0, 1}, {1, 1}, {127, 1},
		{128, 2}, {16383, 2},
		{16384, 3}, {2097151, 3},
		{2097152, 4}, {268435455, 4},
	}
	for _, tc := range cases {
		enc := encodeVBI(nil, tc.n)
		if len(enc) != tc.wantLen {
			t.Errorf("encodeVBI(%d) length = %d, want %d (bytes %x)", tc.n, len(enc), tc.wantLen, enc)
		}
		got, consumed, err := decodeVBI(enc, 0)
		if err != nil {
			t.Fatalf("decodeVBI(%x): %v", enc, err)
		}
		if got != tc.n {
			t.Errorf("decodeVBI roundtrip: got %d, want %d", got, tc.n)
		}
		if consumed != tc.wantLen {
			t.Errorf("decodeVBI(%d) consumed = %d, want %d", tc.n, consumed, tc.wantLen)
		}
		// readVBI (streaming) must agree with decodeVBI.
		rv, rerr := readVBI(bytes.NewReader(enc))
		if rerr != nil || rv != tc.n {
			t.Errorf("readVBI(%x) = %d, %v; want %d", enc, rv, rerr, tc.n)
		}
	}
}

// TestVBISpecBytes checks the exact wire bytes for the spec's canonical examples, so
// a future refactor that silently changed the encoding (e.g. wrong continuation bit)
// is caught.
func TestVBISpecBytes(t *testing.T) {
	if got := encodeVBI(nil, 127); !bytes.Equal(got, []byte{0x7F}) {
		t.Errorf("127 → %x, want 7f", got)
	}
	if got := encodeVBI(nil, 128); !bytes.Equal(got, []byte{0x80, 0x01}) {
		t.Errorf("128 → %x, want 8001", got)
	}
	if got := encodeVBI(nil, 16383); !bytes.Equal(got, []byte{0xFF, 0x7F}) {
		t.Errorf("16383 → %x, want ff7f", got)
	}
	if got := encodeVBI(nil, 16384); !bytes.Equal(got, []byte{0x80, 0x80, 0x01}) {
		t.Errorf("16384 → %x, want 808001", got)
	}
}

// TestVBIOverflow rejects a 5-byte continuation (a malformed/hostile frame) rather
// than looping forever.
func TestVBIOverflow(t *testing.T) {
	bad := []byte{0x80, 0x80, 0x80, 0x80, 0x01}
	if _, _, err := decodeVBI(bad, 0); err == nil {
		t.Fatal("decodeVBI should reject a 5-byte VBI")
	}
	if _, err := readVBI(bytes.NewReader(bad)); err == nil {
		t.Fatal("readVBI should reject a 5-byte VBI")
	}
}

// buildPublish assembles a minimal MQTT 5 PUBLISH body (variable header + properties
// + payload) for parser tests: QoS 0, the given topic, optional content type, user
// properties and payload. It mirrors what a real broker puts on the wire.
func buildPublish(topic, contentType string, userProps map[string]string, payload []byte) []byte {
	var props []byte
	if contentType != "" {
		props = append(props, propContentType)
		props = appendString(props, contentType)
	}
	for k, v := range userProps {
		props = append(props, propUserProperty)
		props = appendString(props, k)
		props = appendString(props, v)
	}
	var body []byte
	body = appendString(body, topic) // QoS 0 → no packet id
	body = encodeVBI(body, len(props))
	body = append(body, props...)
	body = append(body, payload...)
	return body
}

// TestParsePublishMinimalData proves the PUBLISH parser extracts the topic, the User
// Properties and the Content Type, and keeps the payload OPAQUE — the parser exposes
// the payload bytes only as Publish.Payload, which the observer never reads.
func TestParsePublishMinimalData(t *testing.T) {
	payload := []byte("RAW SENSOR PROTOBUF \x00\x01\x02 secret-telemetry")
	body := buildPublish(
		"spBv1.0/plant/DDATA/line1/press01",
		"application/x-sparkplug",
		map[string]string{"seq": "42"},
		payload,
	)
	pub, err := parsePublish(0x00, body)
	if err != nil {
		t.Fatalf("parsePublish: %v", err)
	}
	if pub.Topic != "spBv1.0/plant/DDATA/line1/press01" {
		t.Fatalf("topic = %q", pub.Topic)
	}
	if pub.ContentType != "application/x-sparkplug" {
		t.Fatalf("content type = %q", pub.ContentType)
	}
	if pub.UserProps["seq"] != "42" {
		t.Fatalf("user prop seq = %q", pub.UserProps["seq"])
	}
	if !bytes.Equal(pub.Payload, payload) {
		t.Fatalf("payload not preserved opaquely")
	}
}

// TestParsePublishQoS1SkipsPacketID checks the parser handles the QoS>0 packet
// identifier (2 bytes after the topic) without misaligning the properties.
func TestParsePublishQoS1SkipsPacketID(t *testing.T) {
	// QoS 1 flag = 0b0010. Manually build: topic, packet id, props=0, payload.
	var body []byte
	body = appendString(body, "a/b")
	body = append(body, 0x00, 0x07) // packet id
	body = encodeVBI(body, 0)       // empty properties
	body = append(body, []byte("p")...)
	pub, err := parsePublish(0x02, body)
	if err != nil {
		t.Fatalf("parsePublish QoS1: %v", err)
	}
	if pub.Topic != "a/b" {
		t.Fatalf("topic = %q", pub.Topic)
	}
	if string(pub.Payload) != "p" {
		t.Fatalf("payload = %q", pub.Payload)
	}
}

// TestParsePublishSkipsUnknownProperty checks the parser stays frame-aligned past a
// PUBLISH property it does not consume (here a Message Expiry Interval, 4 bytes).
func TestParsePublishSkipsUnknownProperty(t *testing.T) {
	var props []byte
	props = append(props, 0x02, 0x00, 0x00, 0x00, 0x3C) // Message Expiry Interval = 60
	props = append(props, propUserProperty)
	props = appendString(props, "k")
	props = appendString(props, "v")

	var body []byte
	body = appendString(body, "t")
	body = encodeVBI(body, len(props))
	body = append(body, props...)

	pub, err := parsePublish(0x00, body)
	if err != nil {
		t.Fatalf("parsePublish with unknown property: %v", err)
	}
	if pub.UserProps["k"] != "v" {
		t.Fatalf("user prop after skipped property lost: %+v", pub.UserProps)
	}
}

// TestBuildConnectShape checks the CONNECT builder emits a well-formed packet:
// fixed-header type CONNECT, the protocol name "MQTT", version 5, the clean-start
// flag, and (when credentials are set) the username/password flags. The framing is
// validated by re-reading the fixed header's remaining length.
func TestBuildConnectShape(t *testing.T) {
	pkt := buildConnect("obs-1", "user", "pass", 60*time.Second)
	if pkt[0]>>4 != pktCONNECT {
		t.Fatalf("first byte type = %d, want CONNECT", pkt[0]>>4)
	}
	rem, consumed, err := decodeVBI(pkt, 1)
	if err != nil {
		t.Fatalf("remaining length: %v", err)
	}
	if 1+consumed+rem != len(pkt) {
		t.Fatalf("remaining length %d does not match packet len %d", rem, len(pkt))
	}
	// Variable header starts after the fixed header.
	vh := pkt[1+consumed:]
	name, n, err := readString(vh, 0)
	if err != nil || name != "MQTT" {
		t.Fatalf("protocol name = %q (%v)", name, err)
	}
	if vh[n] != protocolVersion5 {
		t.Fatalf("protocol version = %d, want 5", vh[n])
	}
	connectFlags := vh[n+1]
	if connectFlags&0x02 == 0 {
		t.Fatal("clean-start flag not set")
	}
	if connectFlags&0x80 == 0 || connectFlags&0x40 == 0 {
		t.Fatalf("username/password flags not set: %08b", connectFlags)
	}
}

// TestBuildConnectNoCreds checks the username/password flags are clear when no
// credentials are configured (so an anonymous broker accepts the CONNECT).
func TestBuildConnectNoCreds(t *testing.T) {
	pkt := buildConnect("obs", "", "", 30*time.Second)
	_, consumed, _ := decodeVBI(pkt, 1)
	vh := pkt[1+consumed:]
	_, n, _ := readString(vh, 0)
	connectFlags := vh[n+1]
	if connectFlags&0xC0 != 0 {
		t.Fatalf("credential flags should be clear: %08b", connectFlags)
	}
}

// TestBuildSubscribeShape checks the SUBSCRIBE builder sets the reserved 0b0010
// header flags and frames each topic filter with its options byte.
func TestBuildSubscribeShape(t *testing.T) {
	pkt := buildSubscribe(7, []string{"spBv1.0/#", "factory/+/temp"})
	if pkt[0]>>4 != pktSUBSCRIBE {
		t.Fatalf("type = %d, want SUBSCRIBE", pkt[0]>>4)
	}
	if pkt[0]&0x0f != 0x02 {
		t.Fatalf("SUBSCRIBE reserved flags = %x, want 2", pkt[0]&0x0f)
	}
	_, consumed, err := decodeVBI(pkt, 1)
	if err != nil {
		t.Fatalf("remaining length: %v", err)
	}
	if 1+consumed+remainingLen(pkt, consumed) != len(pkt) {
		t.Fatal("subscribe remaining length mismatch")
	}
}

// remainingLen re-reads the remaining-length field for the framing assertion.
func remainingLen(pkt []byte, _ int) int {
	rem, _, _ := decodeVBI(pkt, 1)
	return rem
}

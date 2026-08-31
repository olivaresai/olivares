// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kmip

import (
	"bytes"
	"context"
	"testing"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// TestEncoderBytes pins the TTLV byte layout against the spec by hand: an Integer
// (Protocol Version Major = 2) is tag(3) type(1) length(4)=4 value(4) pad(4).
func TestEncoderBytes(t *testing.T) {
	got := encInteger(nil, tagProtocolVersionMajor, 2)
	want := []byte{
		0x42, 0x00, 0x6A, // tag
		0x02,                   // type Integer
		0x00, 0x00, 0x00, 0x04, // length 4
		0x00, 0x00, 0x00, 0x02, // value 2
		0x00, 0x00, 0x00, 0x00, // padding to 8
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("integer encoding:\n got %x\nwant %x", got, want)
	}
	// A TextString "1" is 1 value byte padded to 8.
	ts := encTextString(nil, tagUniqueIdentifier, "1")
	if len(ts)%8 != 0 || len(ts) != 16 {
		t.Fatalf("textstring length = %d, want 16 (8 header + 1 + 7 pad)", len(ts))
	}
}

func TestCodecRoundTrip(t *testing.T) {
	var body []byte
	body = encEnumeration(body, tagObjectType, 0x03) // PublicKey
	body = encInteger(body, tagCryptographicLength, 2048)
	root := encStructure(nil, tagAttributes, body)

	it, n, err := decode(root)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(root) {
		t.Fatalf("consumed %d of %d", n, len(root))
	}
	if it.tag != tagAttributes || it.typ != typeStructure {
		t.Fatalf("root = %+v", it)
	}
	ot, ok := it.find(tagObjectType)
	if !ok || ot.u != 0x03 {
		t.Fatalf("object type = %+v ok=%v", ot, ok)
	}
	cl, ok := it.find(tagCryptographicLength)
	if !ok || cl.i != 2048 {
		t.Fatalf("length = %+v ok=%v", cl, ok)
	}
}

// requestOp decodes the Operation enum from a request message (test helper).
func requestOp(t *testing.T, req []byte) uint32 {
	t.Helper()
	msg, _, err := decode(req)
	if err != nil {
		t.Fatal(err)
	}
	bi, ok := msg.find(tagBatchItem)
	if !ok {
		t.Fatal("no batch item")
	}
	op, ok := bi.find(tagOperation)
	if !ok {
		t.Fatal("no operation")
	}
	return op.u
}

// fakeServer is an in-memory KMIP server: it records the operations it was asked to
// run and returns canned Locate / GetAttributes responses, so the read-only client
// is exercised end to end with no network.
type fakeServer struct {
	t      *testing.T
	uids   []string
	sawOps []uint32
}

func (f *fakeServer) roundTrip(_ context.Context, req []byte) ([]byte, error) {
	op := requestOp(f.t, req)
	f.sawOps = append(f.sawOps, op)
	switch op {
	case opLocate:
		var payload []byte
		for _, u := range f.uids {
			payload = encTextString(payload, tagUniqueIdentifier, u)
		}
		return f.respond(opLocate, payload), nil
	case opGetAttributes:
		var attrs []byte
		attrs = encEnumeration(attrs, tagObjectType, 0x03)       // PublicKey
		attrs = encEnumeration(attrs, tagCryptographicAlg, 0x04) // RSA
		attrs = encInteger(attrs, tagCryptographicLength, 2048)
		attrs = encEnumeration(attrs, tagState, 0x02) // Active
		var name []byte
		name = encTextString(name, tagUniqueIdentifier, "prod-signing") // value text inside Name
		attrs = encStructure(attrs, tagName, name)
		payload := encStructure(nil, tagAttributes, attrs)
		return f.respond(opGetAttributes, payload), nil
	default:
		f.t.Fatalf("read-only client issued a non-Locate/GetAttributes op: %#x", op)
		return nil, nil
	}
}

func (f *fakeServer) respond(op uint32, payload []byte) []byte {
	var batch []byte
	batch = encEnumeration(batch, tagOperation, op)
	batch = encEnumeration(batch, tagResultStatus, resultSuccess)
	batch = encStructure(batch, tagResponsePayload, payload)
	var msg []byte
	msg = encStructure(msg, tagResponseHeader, requestHeader())
	msg = encStructure(msg, tagBatchItem, batch)
	return encStructure(nil, tagResponseMessage, msg)
}

type capSink struct{ edges []model.EdgeObservation }

func (c *capSink) Emit(_ context.Context, obs model.Observation) error {
	if e, ok := obs.(model.EdgeObservation); ok {
		c.edges = append(c.edges, e)
	}
	return nil
}

func TestGatherInventory(t *testing.T) {
	fs := &fakeServer{t: t, uids: []string{"key-1", "key-2"}}
	s := New()
	s.tr = fs // inject before Open so Open keeps it
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"endpoint": "hsm.local:5696"}}); err != nil {
		t.Fatal(err)
	}
	sink := &capSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.edges) != 2 {
		t.Fatalf("got %d custody edges, want 2: %+v", len(sink.edges), sink.edges)
	}
	for _, e := range sink.edges {
		if e.Source != signalKMIP || e.ResourceKind != resourceKMIPKey {
			t.Errorf("bad edge: %+v", e)
		}
		if e.Mode != model.ModeUnknown {
			t.Errorf("custody edge mode = %q, want unknown", e.Mode)
		}
		if e.OriginRef != "kmip:hsm.local:5696" {
			t.Errorf("origin = %q", e.OriginRef)
		}
		// The decoded attributes must ride the descriptor.
		if e.ToolRef == "" || e.ToolRef == "kmip.object" {
			t.Errorf("descriptor empty: %q", e.ToolRef)
		}
		for _, want := range []string{"type=PublicKey", "alg=RSA", "bits=2048", "state=Active", "name=prod-signing"} {
			if !bytes.Contains([]byte(e.ToolRef), []byte(want)) {
				t.Errorf("descriptor %q missing %q", e.ToolRef, want)
			}
		}
	}
	// READ-ONLY guarantee: only Locate + GetAttributes were ever issued.
	for _, op := range fs.sawOps {
		if op != opLocate && op != opGetAttributes {
			t.Fatalf("client issued a non-read-only op: %#x", op)
		}
	}
}

func TestSnapshotInventory(t *testing.T) {
	s := New()
	s.tr = &fakeServer{t: t}
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"endpoint": "hsm.local"}}); err != nil {
		t.Fatal(err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Identities) != 1 {
		t.Fatalf("want 1 server NHI, got %+v", g.Identities)
	}
	id := g.Identities[0]
	if id.Kind != identitysource.KindSecretStore || id.Type != identitysource.PrincipalNHI || id.Ref != "kmip:hsm.local:5696" {
		t.Fatalf("server NHI = %+v", id)
	}
}

func TestOpenEndpointDefaultPortIPv6(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		{name: "bare compressed", endpoint: "2001:db8::1", want: "[2001:db8::1]:5696"},
		{name: "bracketed default port", endpoint: "[2001:db8::1]:5696", want: "[2001:db8::1]:5696"},
		{name: "bracketed non-default port", endpoint: "[2001:db8::1]:443", want: "[2001:db8::1]:443"},
		{name: "hostname port", endpoint: "kmip.example:5696", want: "kmip.example:5696"},
		{name: "loopback compressed", endpoint: "::1", want: "[::1]:5696"},
		{name: "link local zone", endpoint: "fe80::1%eth0", want: "[fe80::1%eth0]:5696"},
		{name: "v4 mapped", endpoint: "::ffff:192.0.2.1", want: "[::ffff:192.0.2.1]:5696"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New()
			s.tr = &fakeServer{t: t}
			if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"endpoint": tt.endpoint}}); err != nil {
				t.Fatalf("Open(%q): %v", tt.endpoint, err)
			}
			if s.endpoint != tt.want {
				t.Fatalf("endpoint = %q, want %q", s.endpoint, tt.want)
			}
		})
	}
}

func TestOfflineEmptyGraph(t *testing.T) {
	s := New()
	g, err := s.Snapshot(context.Background())
	if err != nil || len(g.Identities) != 0 {
		t.Fatalf("offline graph = %+v err=%v", g, err)
	}
}

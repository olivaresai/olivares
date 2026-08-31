// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package schemaregistry

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"strings"
	"testing"
)

func confluentValue(id uint32, payload []byte) []byte {
	b := make([]byte, 5+len(payload))
	b[0] = magicByte
	binary.BigEndian.PutUint32(b[1:5], id)
	copy(b[5:], payload)
	return b
}

func TestParseConfluentValueClassic(t *testing.T) {
	v := confluentValue(42, []byte{0x10, 0x20})
	ref, rest, err := ParseConfluentValue(v)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.HasGUID || ref.IntID != 42 {
		t.Fatalf("ref = %+v, want IntID 42", ref)
	}
	if string(rest) != string([]byte{0x10, 0x20}) {
		t.Fatalf("payload not returned verbatim: %v", rest)
	}
}

func TestParseConfluentValueRejectsUnframed(t *testing.T) {
	if _, _, err := ParseConfluentValue([]byte("plain json")); err != ErrNotWireFormatted {
		t.Fatalf("want ErrNotWireFormatted, got %v", err)
	}
	if _, _, err := ParseConfluentValue([]byte{0x01, 0, 0, 0, 1}); err != ErrNotWireFormatted {
		t.Fatalf("non-zero magic byte must be rejected, got %v", err)
	}
}

func TestParseProtobufIndexes(t *testing.T) {
	// Optimized single [0] is a single 0 byte.
	idx, payload := ParseProtobufIndexes([]byte{0x00, 0xAA})
	if len(idx) != 1 || idx[0] != 0 {
		t.Fatalf("single-0 optimization: idx=%v", idx)
	}
	if len(payload) != 1 || payload[0] != 0xAA {
		t.Fatalf("payload after index wrong: %v", payload)
	}
	// length=1, index=2 → zig-zag(1)=2 byte 0x02, zig-zag(2)=4 byte 0x04.
	idx2, payload2 := ParseProtobufIndexes([]byte{0x02, 0x04, 0xBB})
	if len(idx2) != 1 || idx2[0] != 2 {
		t.Fatalf("multi index: idx=%v", idx2)
	}
	if len(payload2) != 1 || payload2[0] != 0xBB {
		t.Fatalf("payload after multi index wrong: %v", payload2)
	}
}

func TestGUIDFromHeaders(t *testing.T) {
	g := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	// Configured key, exact 16 bytes.
	ref, ok := GUIDFromHeaders(map[string][]byte{"my-guid": g}, "my-guid")
	if !ok || !ref.HasGUID || ref.GUID != [16]byte(g) {
		t.Fatalf("configured-key 16-byte: ok=%v ref=%+v", ok, ref)
	}
	// Heuristic: 17 bytes (leading version byte) under an unknown key.
	v17 := append([]byte{0x01}, g...)
	ref2, ok2 := GUIDFromHeaders(map[string][]byte{"x-unknown": v17}, "")
	if !ok2 || ref2.GUID != [16]byte(g) {
		t.Fatalf("heuristic 17-byte: ok=%v ref=%+v", ok2, ref2)
	}
	// Configured key absent → no match (does not fall back to scanning).
	if _, ok := GUIDFromHeaders(map[string][]byte{"other": g}, "my-guid"); ok {
		t.Fatalf("configured key absent should not match another header")
	}
	// No GUID-shaped header → not ok.
	if _, ok := GUIDFromHeaders(map[string][]byte{"k": []byte("short")}, ""); ok {
		t.Fatalf("non-GUID header must not match")
	}
}

// stubDoer returns a canned schema document and records the requested paths.
type stubDoer struct {
	body  string
	paths []string
	code  int
}

func (s *stubDoer) Do(req *http.Request) (*http.Response, error) {
	s.paths = append(s.paths, req.URL.Path)
	code := s.code
	if code == 0 {
		code = 200
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Header:     http.Header{},
	}, nil
}

func TestClientResolveByIDCaches(t *testing.T) {
	doer := &stubDoer{body: `{"schema":"{\"type\":\"record\",\"name\":\"Order\",\"fields\":[{\"name\":\"id\"}]}","schemaType":"AVRO","subject":"orders-value","version":3}`}
	c := NewClient(Options{BaseURL: "https://sr.corp", HTTP: doer})
	for i := 0; i < 2; i++ {
		s, err := c.Resolve(context.Background(), Reference{IntID: 7})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if s.Subject != "orders-value" || s.Type != "AVRO" {
			t.Fatalf("schema = %+v", s)
		}
	}
	if len(doer.paths) != 1 {
		t.Fatalf("expected one fetch (then cache), got %d: %v", len(doer.paths), doer.paths)
	}
	if doer.paths[0] != "/schemas/ids/7" {
		t.Fatalf("by-id path wrong: %s", doer.paths[0])
	}
}

func TestClientResolveByGUIDPath(t *testing.T) {
	doer := &stubDoer{body: `{"schema":"{\"type\":\"record\",\"name\":\"X\"}","schemaType":"AVRO"}`}
	c := NewClient(Options{BaseURL: "https://sr.corp", HTTP: doer})
	var g [16]byte
	for i := range g {
		g[i] = byte(i)
	}
	if _, err := c.Resolve(context.Background(), Reference{GUID: g, HasGUID: true}); err != nil {
		t.Fatalf("resolve guid: %v", err)
	}
	if !strings.HasPrefix(doer.paths[0], "/schemas/guids/") {
		t.Fatalf("by-guid path wrong: %s", doer.paths[0])
	}
}

func TestStructuralRefs(t *testing.T) {
	avro := Schema{Type: "AVRO", Definition: `{"type":"record","name":"OrderCreated","namespace":"com.acme","fields":[{"name":"order_id","type":"string"},{"name":"total","type":"double"}]}`}
	s := StructuralRefs(avro)
	if s.FullName() != "com.acme.OrderCreated" {
		t.Fatalf("avro full name: %q", s.FullName())
	}
	if len(s.Fields) != 2 || s.Fields[0] != "order_id" {
		t.Fatalf("avro fields: %v", s.Fields)
	}

	proto := Schema{Type: "PROTOBUF", Definition: "syntax = \"proto3\";\npackage acme.orders;\nmessage OrderCreated {\n  string order_id = 1;\n  repeated string items = 2;\n}\nmessage Other { string x = 1; }"}
	ps := StructuralRefs(proto)
	if ps.Name != "OrderCreated" || ps.Namespace != "acme.orders" {
		t.Fatalf("proto name/ns: %+v", ps)
	}
	if len(ps.Fields) != 2 || ps.Fields[0] != "order_id" || ps.Fields[1] != "items" {
		t.Fatalf("proto fields (must not include the second message's): %v", ps.Fields)
	}

	jsonsch := Schema{Type: "JSON", Definition: `{"title":"Order","type":"object","properties":{"id":{"type":"string"}}}`}
	js := StructuralRefs(jsonsch)
	if js.Name != "Order" || len(js.Fields) != 1 || js.Fields[0] != "id" {
		t.Fatalf("json schema: %+v", js)
	}
}

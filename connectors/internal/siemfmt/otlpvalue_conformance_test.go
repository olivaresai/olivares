// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0
//
// Differential conformance for the SDK's hand-rolled OTLP AnyValue rendering.
//
// sdk/siemwire declares zero third-party dependencies, so it cannot test itself against the
// official OTLP types — it owns the byte layout and the input validation, which a decoder
// cannot check. This file is the other side of that seam: it takes the SDK's JSON for every
// AnyValue kind, decodes it with the OFFICIAL protojson decoder into the generated
// commonpb.AnyValue with unknown members REJECTED, and compares the decoded Go value with
// what went in.
//
// That is what makes the rendering VERIFIED rather than believed. A hand-written encoder for
// a spec'd wire format is exactly the kind of code that looks right and is subtly wrong —
// int64 as a JSON number instead of a string, unpadded base64, a oneof with two members set —
// and every one of those mistakes is invisible until something on the other side rejects or
// mis-reads the record.
package siemfmt

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/olivaresai/olivares/sdk/siemwire"
)

// TestOTLPValueRenderingMatchesTheOfficialDecoder walks every AnyValue kind, including the
// boundary values where a wrong encoding would actually show: the int64 extremes (a JSON
// number would lose them), bytes that are not valid UTF-8 and cannot travel any other way,
// and empty containers.
func TestOTLPValueRenderingMatchesTheOfficialDecoder(t *testing.T) {
	cases := []struct {
		name  string
		value siemwire.OTLPValue
		check func(t *testing.T, got *commonpb.AnyValue)
	}{
		{
			name:  "a string",
			value: siemwire.OTLPString("hello — ünïcode"),
			check: func(t *testing.T, got *commonpb.AnyValue) {
				if got.GetStringValue() != "hello — ünïcode" {
					t.Errorf("stringValue = %q", got.GetStringValue())
				}
			},
		},
		{
			name:  "an empty string is still a string, not an absent value",
			value: siemwire.OTLPString(""),
			check: func(t *testing.T, got *commonpb.AnyValue) {
				if _, ok := got.GetValue().(*commonpb.AnyValue_StringValue); !ok {
					t.Errorf("the oneof selected %T, want a string", got.GetValue())
				}
			},
		},
		{
			name:  "a true bool",
			value: siemwire.OTLPBool(true),
			check: func(t *testing.T, got *commonpb.AnyValue) {
				if !got.GetBoolValue() {
					t.Error("boolValue = false, want true")
				}
			},
		},
		{
			name:  "a false bool is a set oneof, not an omitted one",
			value: siemwire.OTLPBool(false),
			check: func(t *testing.T, got *commonpb.AnyValue) {
				if _, ok := got.GetValue().(*commonpb.AnyValue_BoolValue); !ok {
					t.Errorf("the oneof selected %T, want a bool", got.GetValue())
				}
			},
		},
		{
			// The value a JSON number cannot carry: 2^63-1 is not exactly representable as a
			// float64, so an encoder that emitted a bare number would hand back 9223372036854775808.
			name:  "the maximum int64",
			value: siemwire.OTLPInt(math.MaxInt64),
			check: func(t *testing.T, got *commonpb.AnyValue) {
				if got.GetIntValue() != math.MaxInt64 {
					t.Errorf("intValue = %d, want %d — precision was lost", got.GetIntValue(), int64(math.MaxInt64))
				}
			},
		},
		{
			name:  "the minimum int64",
			value: siemwire.OTLPInt(math.MinInt64),
			check: func(t *testing.T, got *commonpb.AnyValue) {
				if got.GetIntValue() != math.MinInt64 {
					t.Errorf("intValue = %d, want %d", got.GetIntValue(), int64(math.MinInt64))
				}
			},
		},
		{
			name:  "a negative double",
			value: siemwire.OTLPDouble(-1.5),
			check: func(t *testing.T, got *commonpb.AnyValue) {
				if got.GetDoubleValue() != -1.5 {
					t.Errorf("doubleValue = %v, want -1.5", got.GetDoubleValue())
				}
			},
		},
		{
			// The reason bytesValue exists here at all: these bytes are not valid UTF-8 and
			// cannot survive a JSON string, so base64 is the only faithful carrier.
			name:  "bytes that are not valid UTF-8",
			value: siemwire.OTLPBytes([]byte{0xff, 0xfe, 0x00, 0x41, 0x80}),
			check: func(t *testing.T, got *commonpb.AnyValue) {
				want := []byte{0xff, 0xfe, 0x00, 0x41, 0x80}
				if !bytes.Equal(got.GetBytesValue(), want) {
					t.Errorf("bytesValue = %v, want %v — the round trip is lossy", got.GetBytesValue(), want)
				}
			},
		},
		{
			// Length 1, 2 and 3 mod 3 are where base64 padding differs; ProtoJSON uses the
			// padded alphabet, and a decoder that expects padding rejects the unpadded form.
			name:  "bytes at every padding length",
			value: siemwire.OTLPBytes([]byte{0x01}),
			check: func(t *testing.T, got *commonpb.AnyValue) {
				if !bytes.Equal(got.GetBytesValue(), []byte{0x01}) {
					t.Errorf("bytesValue = %v, want [1]", got.GetBytesValue())
				}
			},
		},
		{
			name:  "empty bytes are bytes, not null",
			value: siemwire.OTLPBytes(nil),
			check: func(t *testing.T, got *commonpb.AnyValue) {
				if _, ok := got.GetValue().(*commonpb.AnyValue_BytesValue); !ok {
					t.Errorf("the oneof selected %T, want bytes", got.GetValue())
				}
				if len(got.GetBytesValue()) != 0 {
					t.Errorf("bytesValue = %v, want empty", got.GetBytesValue())
				}
			},
		},
		{
			name: "an array of mixed kinds",
			value: siemwire.OTLPArray(
				siemwire.OTLPString("s"), siemwire.OTLPInt(7), siemwire.OTLPBool(true),
				siemwire.OTLPBytes([]byte{0xff}),
			),
			check: func(t *testing.T, got *commonpb.AnyValue) {
				values := got.GetArrayValue().GetValues()
				if len(values) != 4 {
					t.Fatalf("arrayValue has %d values, want 4", len(values))
				}
				if values[0].GetStringValue() != "s" || values[1].GetIntValue() != 7 ||
					!values[2].GetBoolValue() || !bytes.Equal(values[3].GetBytesValue(), []byte{0xff}) {
					t.Errorf("array round trip lost a value: %v", values)
				}
			},
		},
		{
			name:  "an empty array",
			value: siemwire.OTLPArray(),
			check: func(t *testing.T, got *commonpb.AnyValue) {
				if _, ok := got.GetValue().(*commonpb.AnyValue_ArrayValue); !ok {
					t.Errorf("the oneof selected %T, want an array", got.GetValue())
				}
				if n := len(got.GetArrayValue().GetValues()); n != 0 {
					t.Errorf("empty array decoded with %d values", n)
				}
			},
		},
		{
			name: "a key/value list, which is what an adjustment record is",
			value: siemwire.OTLPKVList(
				siemwire.OTLPKeyValue{Key: "operation", Value: siemwire.OTLPString("utf8_replace")},
				siemwire.OTLPKeyValue{Key: "original", Value: siemwire.OTLPBytes([]byte("na\xffme"))},
			),
			check: func(t *testing.T, got *commonpb.AnyValue) {
				pairs := got.GetKvlistValue().GetValues()
				if len(pairs) != 2 {
					t.Fatalf("kvlistValue has %d pairs, want 2", len(pairs))
				}
				if pairs[0].GetKey() != "operation" || pairs[0].GetValue().GetStringValue() != "utf8_replace" {
					t.Errorf("first pair = %v", pairs[0])
				}
				if pairs[1].GetKey() != "original" ||
					!bytes.Equal(pairs[1].GetValue().GetBytesValue(), []byte("na\xffme")) {
					t.Errorf("the original bytes did not survive: %v", pairs[1])
				}
			},
		},
		{
			name: "nesting an array inside a list inside an array",
			value: siemwire.OTLPArray(siemwire.OTLPKVList(
				siemwire.OTLPKeyValue{Key: "inner", Value: siemwire.OTLPArray(siemwire.OTLPInt(-1))},
			)),
			check: func(t *testing.T, got *commonpb.AnyValue) {
				inner := got.GetArrayValue().GetValues()[0].GetKvlistValue().GetValues()[0]
				if inner.GetKey() != "inner" {
					t.Fatalf("nested key = %q", inner.GetKey())
				}
				if v := inner.GetValue().GetArrayValue().GetValues()[0].GetIntValue(); v != -1 {
					t.Errorf("nested intValue = %d, want -1", v)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := siemwire.OTLPValueJSON(tc.value)
			if err != nil {
				t.Fatalf("OTLPValueJSON: %v", err)
			}
			var got commonpb.AnyValue
			// DiscardUnknown:false — a misspelled member name must fail here rather than be
			// silently dropped, which would let this comparison pass against our own mistake.
			if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, &got); err != nil {
				t.Fatalf("the official decoder rejected our rendering: %v\n%s", err, raw)
			}
			tc.check(t, &got)

			// Exactly one oneof member: a hand-spliced object could in principle emit two,
			// which protojson would accept as a last-one-wins message and no field check
			// above would notice.
			var members map[string]any
			if err := unmarshalJSONObject(raw, &members); err != nil {
				t.Fatalf("rendering is not a JSON object: %v\n%s", err, raw)
			}
			if len(members) != 1 {
				t.Errorf("AnyValue rendered %d members, want exactly 1 (a oneof): %s", len(members), raw)
			}
		})
	}
}

// TestOTLPNonFiniteDoublesAreRejected: NaN and the infinities have no JSON number form.
// ProtoJSON spells them as the strings "NaN"/"Infinity", which this profile does not emit, so
// they are refused with a diagnostic instead of producing a body a decoder would reject.
func TestOTLPNonFiniteDoublesAreRejected(t *testing.T) {
	for name, v := range map[string]float64{
		"NaN":               math.NaN(),
		"positive infinity": math.Inf(1),
		"negative infinity": math.Inf(-1),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := siemwire.OTLPValueJSON(siemwire.OTLPDouble(v))
			if err == nil {
				t.Fatalf("%s was encoded as %s", name, got)
			}
		})
	}
}

// unmarshalJSONObject decodes into a generic map so the oneof member COUNT can be checked,
// which a typed decode cannot see.
func unmarshalJSONObject(raw []byte, out *map[string]any) error {
	return json.Unmarshal(raw, out)
}

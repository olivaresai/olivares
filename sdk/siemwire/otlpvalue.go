// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemwire

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"
)

// --- OTLP AnyValue -------------------------------------------------------------
//
// OTLP attribute values are not strings: they are `AnyValue`, a oneof. This file is the
// standard-library rendering of the seven general arms a log record uses — string, bool, int,
// double, bytes, an array of AnyValue and a list of KeyValue — in their ProtoJSON form. The
// pinned proto has an eighth, `string_value_strindex`, which belongs to the Profiles signal's
// string table and has no meaning here; it is deliberately not modeled.
//
// It exists because a string is not always the honest encoding. Two cases in this
// product need more:
//
//   - Arbitrary BYTES. A value that is not valid UTF-8 cannot travel in a JSON string
//     without being altered, and an audit feed must be able to carry the original for
//     evidence. `bytesValue` is base64 in ProtoJSON, which is lossless.
//   - STRUCTURE. A record that had to be adjusted on the way to the wire needs to say
//     what was adjusted, where, and what the original was. Comma-joined labels in a
//     string cannot express that unambiguously — a caller's own key may contain the
//     delimiter — and cannot carry bytes at all.
//
// A oneof is the one place the package's "always emit the declared member" profile does
// NOT apply: exactly one member is set, so exactly one is emitted. Emitting the others
// would not be a canonical shape, it would be a different message.

// maxOTLPValueDepth bounds how deeply an AnyValue may nest.
//
// It exists because both the validator and the encoder walk the union RECURSIVELY, and Go's
// stack overflow is a FATAL ERROR, not a recoverable panic: a sufficiently nested value
// takes the whole process down. This package is the public, Apache-2.0 SDK that third-party
// connectors build against, so "no caller in this repository nests that deeply" is not a
// guarantee — it is an assumption about code that does not exist yet.
//
// OTLP itself sets no limit. 32 is a chosen defensive budget: well above the one-to-three
// level shapes this repository builds, while leaving the recursion trivially bounded, and
// exceeding it is a precise error naming the path rather than a crash. It is NOT a measured
// universal maximum — nothing here can establish one for every shape a caller might build.
// The limit is declared rather than discovered.
const maxOTLPValueDepth = 32

// otlpValueKind discriminates the AnyValue oneof.
type otlpValueKind uint8

const (
	otlpValueString otlpValueKind = iota
	otlpValueBool
	otlpValueInt
	otlpValueDouble
	otlpValueBytes
	otlpValueArray
	otlpValueKVList
)

// OTLPValue is one OTLP AnyValue. The zero value is the empty string, which is the
// sensible default for an attribute whose value was never set.
type OTLPValue struct {
	kind    otlpValueKind
	str     string
	boolean bool
	integer int64
	double  float64
	bytes   []byte
	array   []OTLPValue
	kvlist  []OTLPKeyValue
}

// OTLPKeyValue is one OTLP KeyValue: a non-empty, unique key and an AnyValue.
type OTLPKeyValue struct {
	Key   string
	Value OTLPValue
}

// OTLPString returns a string AnyValue.
func OTLPString(s string) OTLPValue { return OTLPValue{kind: otlpValueString, str: s} }

// OTLPBool returns a bool AnyValue.
func OTLPBool(b bool) OTLPValue { return OTLPValue{kind: otlpValueBool, boolean: b} }

// OTLPInt returns a 64-bit integer AnyValue. ProtoJSON renders a 64-bit integer as a decimal
// STRING, because a JSON number MAY lose int64 precision in a JavaScript-class consumer: values
// inside the safe-integer range survive, values outside it do not.
func OTLPInt(i int64) OTLPValue { return OTLPValue{kind: otlpValueInt, integer: i} }

// OTLPDouble returns a floating-point AnyValue. NaN and the infinities have no JSON
// number form and are rejected at encode time rather than silently rendered.
func OTLPDouble(f float64) OTLPValue { return OTLPValue{kind: otlpValueDouble, double: f} }

// OTLPBytes returns a bytes AnyValue, which ProtoJSON renders as base64. This is the
// only value kind that can carry arbitrary bytes without alteration, so it is what an
// original value should travel in when the value could not be a JSON string.
func OTLPBytes(b []byte) OTLPValue { return OTLPValue{kind: otlpValueBytes, bytes: b} }

// OTLPArray returns an array AnyValue.
func OTLPArray(values ...OTLPValue) OTLPValue {
	return OTLPValue{kind: otlpValueArray, array: values}
}

// OTLPKVList returns a key/value-list AnyValue. Its keys follow the same rule as an
// attribute set: non-empty and unique.
func OTLPKVList(pairs ...OTLPKeyValue) OTLPValue {
	return OTLPValue{kind: otlpValueKVList, kvlist: pairs}
}

// OTLPStringFields adapts the package's ordinary Field slice — the one the text
// encoders use — to OTLP key/values. It is the bridge for the common case where every
// attribute really is a string, so a caller does not have to spell out OTLPString for
// each one.
func OTLPStringFields(fields []Field) []OTLPKeyValue {
	out := make([]OTLPKeyValue, 0, len(fields))
	for _, f := range fields {
		out = append(out, OTLPKeyValue{Key: f.Key, Value: OTLPString(f.Value)})
	}
	return out
}

// IsString reports whether v is a string value, and returns it. Callers that only
// understand strings (and tests asserting the common case) use this instead of
// reaching into the unexported representation.
func (v OTLPValue) IsString() (string, bool) {
	return v.str, v.kind == otlpValueString
}

// marshalJSON renders the AnyValue as its ProtoJSON object: exactly one member, named
// for the kind that is set.
func (v OTLPValue) marshalJSON(depth int) ([]byte, error) {
	if depth > maxOTLPValueDepth {
		return nil, fmt.Errorf("siemwire: OTLP value nests deeper than %d levels", maxOTLPValueDepth)
	}
	var member string
	var payload any
	switch v.kind {
	case otlpValueString:
		member, payload = "stringValue", v.str
	case otlpValueBool:
		member, payload = "boolValue", v.boolean
	case otlpValueInt:
		// ProtoJSON: 64-bit integers are decimal strings.
		member, payload = "intValue", strconv.FormatInt(v.integer, 10)
	case otlpValueDouble:
		if math.IsNaN(v.double) || math.IsInf(v.double, 0) {
			return nil, fmt.Errorf("siemwire: OTLP doubleValue %v has no JSON number form", v.double)
		}
		member, payload = "doubleValue", v.double
	case otlpValueBytes:
		// encoding/json renders a []byte as standard padded base64 in a JSON string,
		// which is exactly ProtoJSON's bytes encoding. A nil slice would render as
		// null, so an empty slice is substituted to keep one shape.
		b := v.bytes
		if b == nil {
			b = []byte{}
		}
		member, payload = "bytesValue", b
	case otlpValueArray:
		items := make([]json.RawMessage, 0, len(v.array))
		for _, item := range v.array {
			raw, err := item.marshalJSON(depth + 1)
			if err != nil {
				return nil, err
			}
			items = append(items, raw)
		}
		member, payload = "arrayValue", struct {
			Values []json.RawMessage `json:"values"`
		}{Values: items}
	case otlpValueKVList:
		pairs, err := otlpJSONKeyValues(v.kvlist, depth+1)
		if err != nil {
			return nil, err
		}
		member, payload = "kvlistValue", struct {
			Values []json.RawMessage `json:"values"`
		}{Values: pairs}
	default:
		return nil, fmt.Errorf("siemwire: OTLP value kind %d is not a declared AnyValue member", v.kind)
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("siemwire: encode OTLP %s: %w", member, err)
	}
	name, err := json.Marshal(member)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(name)+len(encoded)+3)
	out = append(out, '{')
	out = append(out, name...)
	out = append(out, ':')
	out = append(out, encoded...)
	out = append(out, '}')
	return out, nil
}

// otlpJSONKeyValues renders an attribute set. A nil or empty set yields an empty JSON
// array, never null: `null` and `[]` are two byte forms for "none", and a consumer
// indexing the array should not have to handle both.
func otlpJSONKeyValues(pairs []OTLPKeyValue, depth int) ([]json.RawMessage, error) {
	if depth > maxOTLPValueDepth {
		return nil, fmt.Errorf("siemwire: OTLP attributes nest deeper than %d levels", maxOTLPValueDepth)
	}
	out := make([]json.RawMessage, 0, len(pairs))
	for _, p := range pairs {
		value, err := p.Value.marshalJSON(depth + 1)
		if err != nil {
			return nil, err
		}
		key, err := json.Marshal(p.Key)
		if err != nil {
			return nil, err
		}
		raw := make([]byte, 0, len(key)+len(value)+12)
		raw = append(raw, `{"key":`...)
		raw = append(raw, key...)
		raw = append(raw, `,"value":`...)
		raw = append(raw, value...)
		raw = append(raw, '}')
		out = append(out, raw)
	}
	return out, nil
}

// validateOTLPValue rejects a value the JSON grammar cannot carry faithfully, walking nested
// arrays and lists. `where` is the exact path, so a diagnostic names the offender rather than
// the container, and `depth` bounds the walk — see maxOTLPValueDepth for why an unbounded
// one is not merely inefficient but fatal.
func validateOTLPValue(where string, v OTLPValue, depth int) error {
	if depth > maxOTLPValueDepth {
		return fmt.Errorf("siemwire: %s nests deeper than %d levels; refusing to recurse further",
			where, maxOTLPValueDepth)
	}
	switch v.kind {
	case otlpValueString:
		if !utf8.ValidString(v.str) {
			return fmt.Errorf("siemwire: %s is not valid UTF-8", where)
		}
	case otlpValueDouble:
		if math.IsNaN(v.double) || math.IsInf(v.double, 0) {
			return fmt.Errorf("siemwire: %s is %v, which has no JSON number form", where, v.double)
		}
	case otlpValueArray:
		for i, item := range v.array {
			if err := validateOTLPValue(fmt.Sprintf("%s[%d]", where, i), item, depth+1); err != nil {
				return err
			}
		}
	case otlpValueKVList:
		if err := validateOTLPKeyValues(where, v.kvlist, depth+1); err != nil {
			return err
		}
	}
	// bytesValue, boolValue and intValue have no invalid form: any byte sequence is
	// base64-encodable, and both scalars always render.
	return nil
}

// validateOTLPKeyValues checks one attribute set: every key non-empty, unique and valid
// UTF-8, and every value carryable. OTLP states that attribute keys MUST be unique
// within a set and that a receiver's behavior on duplicates is unpredictable, so a
// duplicate is an encoding error here rather than something to pass on and hope about.
func validateOTLPKeyValues(where string, pairs []OTLPKeyValue, depth int) error {
	if depth > maxOTLPValueDepth {
		return fmt.Errorf("siemwire: %s nests deeper than %d levels; refusing to recurse further",
			where, maxOTLPValueDepth)
	}
	seen := make(map[string]struct{}, len(pairs))
	for i, p := range pairs {
		at := fmt.Sprintf("%s[%d]", where, i)
		if p.Key == "" {
			return fmt.Errorf("siemwire: %s has an empty key", at)
		}
		if !utf8.ValidString(p.Key) {
			return fmt.Errorf("siemwire: %s key is not valid UTF-8", at)
		}
		if _, dup := seen[p.Key]; dup {
			return fmt.Errorf("siemwire: %s key %q appears more than once (OTLP requires unique keys)", where, p.Key)
		}
		seen[p.Key] = struct{}{}
		if err := validateOTLPValue(fmt.Sprintf("%s value for %q", where, p.Key), p.Value, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// OTLPValueJSON renders one AnyValue as its ProtoJSON object. It is exported so a caller
// projecting the same value into the generated protobuf types can decode this rendering
// instead of writing a second switch over the union that could drift from this one.
func OTLPValueJSON(v OTLPValue) ([]byte, error) {
	if err := validateOTLPValue("OTLP value", v, 0); err != nil {
		return nil, err
	}
	return v.marshalJSON(0)
}

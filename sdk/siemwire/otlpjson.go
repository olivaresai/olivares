// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemwire

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
	"unicode/utf8"
)

// --- OTLP/HTTP JSON ------------------------------------------------------------
//
// The OTLP *protobuf* types deliberately stay out of this package: they would break
// the SDK's zero-third-party-dependency guarantee (see the package doc). What lives
// here is the JSON wire GRAMMAR, hand-rolled on the standard library, and the one
// timestamp conversion every OTLP emitter in the product must use.
//
// Hand-rolled rather than generated-types-plus-protojson, for three reasons that were
// each observed rather than assumed:
//
//   - protojson does not promise byte-stable output across library versions, and it
//     says so itself. Golden tests, diffing two records, and any destination that keys
//     on the exact payload all depend on one input producing one byte sequence, so an
//     unannounced dependency bump must not change a record's serialization.
//   - Default protojson encodes a non-zero enum by its NAME. OTLP/JSON requires enum
//     values to be integers and forbids the names, so `"severityNumber":
//     "SEVERITY_NUMBER_ERROR"` is nonconformant output, not a stylistic variant.
//   - Default protojson OMITS zero values, so an unknown severity produced no
//     `severityNumber` member at all and an unset time produced no `timeUnixNano`.
//     That omission is legal OTLP — a proto3 decoder reads an absent scalar and an
//     explicit zero as the same value — but it gives a raw-JSON consumer two shapes
//     for one column, and a SIEM rule filtering that column has to handle both.
//
// Turning on UseEnumNumbers+EmitDefaultValues would fix the first two symptoms but
// also emit every unrelated default scalar, empty list and empty map in the generated
// message graph, which would let a dependency update change our public JSON shape
// without a decision here. So the shape is declared explicitly below.
//
// What that shape IS, stated precisely so no reader mistakes it for a conformance
// requirement: a CANONICAL PROFILE of OTLP/JSON. It declares a chosen subset of the
// LogRecord members and always emits every one of them, defaults included, so a
// declared value has one byte form rather than two. Members it does not declare
// (droppedAttributesCount, flags, traceId, spanId, schemaUrl, scope attributes,
// resource entityRefs) are absent always, which is equally one form. A conforming OTLP
// receiver decodes an omitted proto3 default and an explicit default identically;
// emitting them is OUR decision about raw-shape stability, not something OTLP demands.

// nsPerSecond is the nanosecond scale used to build an OTLP timestamp without going
// through time.UnixNano (see OTLPTime for why that matters).
const nsPerSecond = uint64(time.Second / time.Nanosecond)

// OTLPTimeStatus says what an OTLP timestamp encoding asserts about an instant, so a
// caller can tell apart the situations that all encode to the same byte 0.
type OTLPTimeStatus uint8

const (
	// OTLPTimeAbsent: the input carried no instant (the Go zero time). OTLP's 0
	// ("unknown") is exactly the right statement and nothing is lost.
	OTLPTimeAbsent OTLPTimeStatus = iota
	// OTLPTimeExact: the encoding is the instant, at nanosecond fidelity.
	OTLPTimeExact
	// OTLPTimeAtEpoch: the instant IS 1970-01-01T00:00:00Z, whose encoding is 0 — the
	// same byte OTLP reserves for "unknown". Representable, but indistinguishable on
	// the wire from OTLPTimeAbsent: OTLP cannot express the epoch. A caller that must
	// preserve it has to carry it separately.
	OTLPTimeAtEpoch
	// OTLPTimeBeforeEpoch: the instant precedes 1970-01-01T00:00:00Z, which unsigned
	// nanoseconds cannot express at all. 0 is emitted so no false date is manufactured,
	// and the instant IS LOST unless the caller carries it elsewhere. This used to
	// produce a plausible wrong date in the far future.
	OTLPTimeBeforeEpoch
	// OTLPTimeAfterCeiling: the instant follows 2554-07-21T23:34:33.709551615Z, the last
	// one unsigned nanoseconds can express. Same consequence as OTLPTimeBeforeEpoch, but
	// named separately so a rule can tell the direction without parsing the timestamp:
	// the two failures have different operational causes.
	OTLPTimeAfterCeiling
)

// String returns the stable wire token for the status. It is a token, not a message:
// callers put it in an attribute value, so it must not drift with prose.
func (s OTLPTimeStatus) String() string {
	switch s {
	case OTLPTimeAbsent:
		return "absent"
	case OTLPTimeExact:
		return "exact"
	case OTLPTimeAtEpoch:
		return "epoch_indistinguishable_from_unknown"
	case OTLPTimeBeforeEpoch:
		return "before_epoch"
	case OTLPTimeAfterCeiling:
		return "after_uint64_ceiling"
	default:
		return "unspecified"
	}
}

// OTLPTime converts an instant to OTLP's timestamp encoding — UNSIGNED nanoseconds
// since the Unix epoch, with 0 reserved for "unknown" — AND reports which situation
// produced it. It reports the status because three different inputs encode to 0 and
// only one of them is lossless: a caller that must not lose the time needs to know
// which one it got (see OTLPTimeStatus).
//
// It exists because `uint64(t.UnixNano())` is wrong in three separate ways, all of
// which produce a WRONG DATE that a decoder accepts rather than an error anyone
// notices. UnixNano is int64 and the standard library documents it as undefined
// outside 1678-2262 and for the zero time:
//
//   - The zero time (year 1) casts to a huge positive value.
//   - Any pre-epoch instant does too: 1969-07-20T20:17:00Z becomes
//     18432561093709551616, which reads as 2554-02-07T19:51:33.709551616Z — 585 years
//     off, silently.
//   - Any instant past 2554-07-21T23:34:33.709551615Z — the last one unsigned
//     nanoseconds can express — wraps back to a small positive value:
//     2554-07-21T23:34:34Z becomes 290448384, i.e. 1970-01-01T00:00:00.290448384Z.
//
// The protobuf and JSON wire types are both plain unsigned 64-bit, so neither can
// distinguish a wrapped value from a real one and the generated decoder accepts all
// three. Whether a receiving collector or SIEM applies its own range or business
// validation on top is destination-specific and is not asserted here.
//
// So the value is built from Unix seconds plus the nanosecond remainder, which is exact
// across the whole unsigned range, and each bound is checked BEFORE the arithmetic that
// could overflow past it.
//
// One limit is outside this function's reach, and is stated rather than claimed away: a
// time.Time built from out-of-range components can already have overflowed its own
// internal representation before it is passed in, in which case the instant was
// falsified upstream and this conversion faithfully encodes the falsified value. Values
// from the system clock, from time.Parse of an RFC 3339 timestamp, and from time.Date
// with in-range components are unaffected.
func OTLPTime(t time.Time) (uint64, OTLPTimeStatus) {
	if t.IsZero() {
		return 0, OTLPTimeAbsent
	}
	sec := t.Unix()
	if sec < 0 {
		return 0, OTLPTimeBeforeEpoch
	}
	whole := uint64(sec)
	if whole > math.MaxUint64/nsPerSecond {
		return 0, OTLPTimeAfterCeiling
	}
	nanos := whole * nsPerSecond
	frac := uint64(t.Nanosecond())
	if nanos > math.MaxUint64-frac {
		return 0, OTLPTimeAfterCeiling
	}
	total := nanos + frac
	if total == 0 {
		return 0, OTLPTimeAtEpoch
	}
	return total, OTLPTimeExact
}

// OTLPTimeUnixNano is OTLPTime without the status, for callers that only need the
// number — this file's own field formatting, and any emitter that has already decided
// it carries no fallback. Prefer OTLPTime when losing the instant would matter: this
// signature cannot tell you that it did.
func OTLPTimeUnixNano(t time.Time) uint64 {
	v, _ := OTLPTime(t)
	return v
}

// OTLPRecord is one resolved OTLP LogRecord as this product emits it. Every field is
// already decided by the caller: severity is mapped, attributes are ordered, and the
// timestamps are already guarded values from OTLPTime. siemwire lays out the JSON and
// validates it, and does nothing else — exactly like the text encoders above.
type OTLPRecord struct {
	// TimeUnixNano is when the event occurred; 0 means unknown.
	TimeUnixNano uint64
	// ObservedTimeUnixNano is when the collecting system saw it; 0 means unknown.
	// A caller that does not know it must leave it 0 rather than copying
	// TimeUnixNano, which would assert an observation that never happened.
	ObservedTimeUnixNano uint64
	// SeverityNumber is the OTLP severity enum as a NUMBER (0 = UNSPECIFIED).
	SeverityNumber int32
	// SeverityText is the emitter's own severity label.
	SeverityText string
	// EventName is OTLP's dedicated event-type member (LogRecord.event_name, field 12
	// of the pinned opentelemetry-proto). Its presence identifies the record as an
	// Event rather than a plain log line, which is what a governance notification is.
	// Empty means "not an event".
	EventName string
	// Body is the record's message.
	Body string
	// Attributes are the already-ordered record attributes. siemwire never sorts.
	// Keys must be non-empty and unique: OTLP requires uniqueness and states that a
	// receiver's handling of duplicates is unpredictable. Values are full AnyValues —
	// use OTLPStringFields to adapt a plain Field slice for the common all-strings case.
	Attributes []OTLPKeyValue
}

// OTLPRequest is one complete OTLP/HTTP logs export request: one resource, one named
// and versioned instrumentation scope, and one record. It mirrors
// opentelemetry.proto.collector.logs.v1.ExportLogsServiceRequest in its ProtoJSON
// form, which is the body an OTLP/HTTP endpoint accepts at /v1/logs.
type OTLPRequest struct {
	// ResourceAttributes identify the emitter; already ordered, keys unique.
	ResourceAttributes []OTLPKeyValue
	// ScopeName names the instrumentation scope: the component that PRODUCES the
	// records, not the transport that ships them. A stable, namespaced product-owned
	// name belongs here — naming one of several transports would be false for the
	// others.
	ScopeName string
	// ScopeVersion is the version of that producing component: its own encoder
	// version. It is NOT the service version and NOT an operator-configurable device
	// header — a backend that groups records or selects a schema by scope version is
	// entitled to expect it to identify the emitting code.
	ScopeVersion string
	// Record is the single log record carried by the request.
	Record OTLPRecord
}

// The layout below is the declared JSON shape. Member names are the ProtoJSON lowerCamel
// spellings; 64-bit integers are pre-formatted decimal STRINGS (ProtoJSON encodes 64-bit
// fields as strings, and a bare JSON number also loses precision in JavaScript-class
// consumers); the severity enum stays a number. There is no omitempty on any DECLARED
// member: a declared zero value must have one byte shape, not two. Members not declared
// here are absent always, which is also one shape — see the canonical-profile note at the
// top of this file. The one exception is inside an AnyValue, which is a oneof: exactly one
// member is set, so exactly one is emitted (otlpvalue.go).
type otlpJSONRecord struct {
	TimeUnixNano         string            `json:"timeUnixNano"`
	ObservedTimeUnixNano string            `json:"observedTimeUnixNano"`
	SeverityNumber       int32             `json:"severityNumber"`
	SeverityText         string            `json:"severityText"`
	EventName            string            `json:"eventName"`
	Body                 json.RawMessage   `json:"body"`
	Attributes           []json.RawMessage `json:"attributes"`
}

type otlpJSONResource struct {
	Attributes []json.RawMessage `json:"attributes"`
}

type otlpJSONScope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type otlpJSONScopeLogs struct {
	Scope      otlpJSONScope    `json:"scope"`
	LogRecords []otlpJSONRecord `json:"logRecords"`
}

type otlpJSONResourceLogs struct {
	Resource  otlpJSONResource    `json:"resource"`
	ScopeLogs []otlpJSONScopeLogs `json:"scopeLogs"`
}

type otlpJSONExportRequest struct {
	ResourceLogs []otlpJSONResourceLogs `json:"resourceLogs"`
}

// otlpJSONRecordOf projects a resolved record onto its JSON shape.
func otlpJSONRecordOf(r OTLPRecord) (otlpJSONRecord, error) {
	body, err := OTLPString(r.Body).marshalJSON(0)
	if err != nil {
		return otlpJSONRecord{}, err
	}
	attrs, err := otlpJSONKeyValues(r.Attributes, 0)
	if err != nil {
		return otlpJSONRecord{}, err
	}
	return otlpJSONRecord{
		TimeUnixNano:         strconv.FormatUint(r.TimeUnixNano, 10),
		ObservedTimeUnixNano: strconv.FormatUint(r.ObservedTimeUnixNano, 10),
		SeverityNumber:       r.SeverityNumber,
		SeverityText:         r.SeverityText,
		EventName:            r.EventName,
		Body:                 body,
		Attributes:           attrs,
	}, nil
}

// ValidateOTLPRequest reports whether a request can be encoded faithfully: every string
// valid UTF-8, every attribute key non-empty and unique within its set, every nested value
// carryable. It never repairs.
//
// It is EXPORTED because it is the contract of the request itself, not of one encoding. A
// caller that projects the same request into the generated protobuf types must apply the
// same check, or the two encodings have different contracts and one can emit a message the
// other refuses — duplicate attribute keys marshal happily into protobuf while this
// encoder rejects them.
//
// The UTF-8 rule is the substantive one. encoding/json replaces every invalid UTF-8 byte
// sequence with U+FFFD, so "invalid\xff" and "invalid\xfe" would serialize to identical
// bytes and neither the emitter nor the consumer could tell which value was supplied. For
// an audit feed that silent collapse is worse than an error. A caller that would rather
// deliver an altered record than none must make that substitution itself, in its own policy
// layer, and say on the wire that it did — carrying the original in an OTLPBytes value,
// which is lossless. This layer's contract is that the bytes it emits mean exactly what it
// was handed.
func ValidateOTLPRequest(r OTLPRequest) error {
	// Ordered, not a map: the diagnostic must name the same field on every run.
	for _, text := range []struct {
		name  string
		value string
	}{
		{"scope name", r.ScopeName},
		{"scope version", r.ScopeVersion},
		{"severity text", r.Record.SeverityText},
		{"event name", r.Record.EventName},
		{"body", r.Record.Body},
	} {
		if !utf8.ValidString(text.value) {
			return fmt.Errorf("siemwire: OTLP %s is not valid UTF-8", text.name)
		}
	}
	if err := validateOTLPKeyValues("resource attribute", r.ResourceAttributes, 0); err != nil {
		return err
	}
	return validateOTLPKeyValues("record attribute", r.Record.Attributes, 0)
}

// OTLPExportRequestJSON encodes one complete OTLP/HTTP JSON export request. The bytes are
// byte-deterministic for a given input and are what an OTLP/HTTP endpoint accepts as a POST
// body at /v1/logs. It returns an error rather than silently altering input that JSON
// cannot carry unchanged (see ValidateOTLPRequest).
func OTLPExportRequestJSON(request OTLPRequest) ([]byte, error) {
	if err := ValidateOTLPRequest(request); err != nil {
		return nil, err
	}
	record, err := otlpJSONRecordOf(request.Record)
	if err != nil {
		return nil, err
	}
	resourceAttrs, err := otlpJSONKeyValues(request.ResourceAttributes, 0)
	if err != nil {
		return nil, err
	}
	doc := otlpJSONExportRequest{
		ResourceLogs: []otlpJSONResourceLogs{{
			Resource: otlpJSONResource{Attributes: resourceAttrs},
			ScopeLogs: []otlpJSONScopeLogs{{
				Scope:      otlpJSONScope{Name: request.ScopeName, Version: request.ScopeVersion},
				LogRecords: []otlpJSONRecord{record},
			}},
		}},
	}
	return json.Marshal(doc)
}

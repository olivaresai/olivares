// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package natsbus

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

// TestCodecTypedObservationRoundtrip: the wire trap this codec exists to avoid
// is map[string]any payloads — every typed reader silently drops them. The
// three first-party observation payloads must re-materialize so event.EdgeOf
// (etc.) still answer on the consuming node.
func TestCodecTypedObservationRoundtrip(t *testing.T) {
	at := time.Date(2026, 6, 12, 10, 0, 0, 123456789, time.UTC)
	in := event.Event{
		ID: "id-1", Type: event.TypeEdgeObserved, Tenant: "tn-1", Source: "connector:pg", Time: at,
		Payload: model.EdgeObservation{
			OriginKind: "agent", OriginRef: "a1", ResourceKind: "postgres.table", ResourceRef: "public.t",
			Mode: model.ModeRead, Source: model.SignalOTEL, Confidence: model.ConfidenceAttributed, ObservedAt: at,
		},
	}
	data, err := EncodeEvent(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeEvent(data, DefaultDecoders())
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != in.ID || out.Type != in.Type || out.Tenant != in.Tenant || out.Source != in.Source {
		t.Fatalf("envelope did not roundtrip: %+v", out)
	}
	if !out.Time.Equal(in.Time) {
		t.Fatalf("time must roundtrip to the nanosecond: %v != %v", out.Time, in.Time)
	}
	edge, ok := event.EdgeOf(out)
	if !ok {
		t.Fatalf("EdgeOf must re-materialize on the consuming side; payload is %T", out.Payload)
	}
	if edge.ResourceRef != "public.t" || edge.Mode != model.ModeRead {
		t.Fatalf("edge fields lost: %+v", edge)
	}

	// A pointer DTO publishes too (value receivers — both satisfy Observation).
	in.Payload = &model.CostSample{ProviderRef: "anthropic", ModelRef: "claude-fable-5", CostMicroUSD: 42}
	in.Type = event.TypeCostSampled
	data, err = EncodeEvent(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err = DecodeEvent(data, DefaultDecoders())
	if err != nil {
		t.Fatal(err)
	}
	cost, ok := event.CostOf(out)
	if !ok || cost.CostMicroUSD != 42 {
		t.Fatalf("CostOf must re-materialize: ok=%v %+v", ok, cost)
	}
}

// TestCodecModuleDefinedTypes: SDK-defined module payloads ride json_payload
// and re-materialize via the default decoder registry.
func TestCodecModuleDefinedTypes(t *testing.T) {
	in := event.Event{
		ID: "id-2", Type: event.TypeGuardrailObserved, Tenant: "tn-1", Source: "module:liveingest",
		Payload: event.ObservedText{SessionRef: "s1", AgentRef: "a1", Surface: "tool_args", Text: "ref:/etc/passwd"},
	}
	data, err := EncodeEvent(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeEvent(data, DefaultDecoders())
	if err != nil {
		t.Fatal(err)
	}
	ot, ok := event.ObservedTextOf(out)
	if !ok || ot.Text != "ref:/etc/passwd" {
		t.Fatalf("ObservedTextOf must re-materialize: ok=%v %+v (payload %T)", ok, ot, out.Payload)
	}
}

// TestCodecUnknownTypeAndExtension: an unregistered type decodes to
// json.RawMessage (the tolerant-consumer shape); a composition-root-registered
// decoder re-materializes its concrete type.
func TestCodecUnknownTypeAndExtension(t *testing.T) {
	type custom struct {
		N int    `json:"n"`
		S string `json:"s"`
	}
	in := event.Event{ID: "id-3", Type: "acme.custom", Tenant: "tn-1", Payload: custom{N: 7, S: "x"}}
	data, err := EncodeEvent(in)
	if err != nil {
		t.Fatal(err)
	}

	// Unregistered: raw JSON survives byte-exact for re-marshal consumers.
	out, err := DecodeEvent(data, DefaultDecoders())
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := out.Payload.(json.RawMessage)
	if !ok {
		t.Fatalf("unregistered type must decode to json.RawMessage, got %T", out.Payload)
	}
	var c custom
	if err := json.Unmarshal(raw, &c); err != nil || c.N != 7 {
		t.Fatalf("raw payload must re-unmarshal: %v %+v", err, c)
	}

	// Registered: concrete type back.
	dec := DefaultDecoders()
	dec["acme.custom"] = func(b []byte) (any, error) {
		var v custom
		err := json.Unmarshal(b, &v)
		return v, err
	}
	out, err = DecodeEvent(data, dec)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := out.Payload.(custom); !ok || got.S != "x" {
		t.Fatalf("registered decoder must re-materialize: %T %+v", out.Payload, out.Payload)
	}
}

// TestCodecNilPayload: no payload, no oneof, nil back.
func TestCodecNilPayload(t *testing.T) {
	data, err := EncodeEvent(event.Event{ID: "id-4", Type: "acme.ping", Tenant: "tn-1"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeEvent(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Payload != nil {
		t.Fatalf("nil payload must roundtrip nil, got %T", out.Payload)
	}
}

// FuzzDecodeEvent guards the untrusted cross-node decode boundary: arbitrary
// bytes either produce an event or a zero event plus an error, never a panic.
func FuzzDecodeEvent(f *testing.F) {
	seeds := []event.Event{
		{},
		{
			ID: "edge-seed", Type: event.TypeEdgeObserved, Tenant: "tn-1", Source: "connector:pg",
			Payload: model.EdgeObservation{
				OriginKind: "agent", OriginRef: "a1", ResourceKind: "postgres.table", ResourceRef: "public.t",
				Mode: model.ModeRead, Source: model.SignalOTEL, Confidence: model.ConfidenceAttributed,
			},
		},
		{
			ID: "json-seed", Type: event.TypeGuardrailObserved, Tenant: "tn-1", Source: "module:liveingest",
			Payload: event.ObservedText{SessionRef: "s1", AgentRef: "a1", Surface: "tool_args", Text: "seed"},
		},
	}
	for _, seed := range seeds {
		data, err := EncodeEvent(seed)
		if err != nil {
			f.Fatalf("encode seed: %v", err)
		}
		f.Add(data)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := DecodeEvent(data, DefaultDecoders())
		if err != nil && !reflect.DeepEqual(got, event.Event{}) {
			t.Fatalf("DecodeEvent returned a partial event with an error: event=%+v err=%v", got, err)
		}
	})
}

// TestConfigValidate pins the deny-closed config rules (an invalid config is a
// boot abort, never a silent in-proc fallback).
func TestConfigValidate(t *testing.T) {
	ok := Config{Backend: "nats", URL: "nats://h:4222"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if ok.Name != DefaultName || ok.SubjectPrefix != DefaultSubjectPrefix {
		t.Fatalf("defaults not applied: %+v", ok)
	}
	bad := []Config{
		{Backend: "", URL: "nats://h:4222"},                // unknown backend
		{Backend: "kafka", URL: "x"},                       // unknown backend
		{Backend: "nats"},                                  // missing url
		{Backend: "nats", URL: "u", SubjectPrefix: "a..b"}, // empty token
		{Backend: "nats", URL: "u", SubjectPrefix: "a.*"},  // wildcard
		{Backend: "nats", URL: "u", SubjectPrefix: "a b"},  // whitespace
		{Backend: "nats", URL: "u", TLSCertFile: "c.pem"},  // cert without key
		{Backend: "nats", URL: "u", TLSKeyFile: "k.pem"},   // key without cert
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("bad config %d must be rejected: %+v", i, c)
		}
	}
}

// TestSubjectValidation pins the publish-side type→subject rules.
func TestSubjectValidation(t *testing.T) {
	for _, good := range []string{"edge.observed", "voice.telemetry.observed", "acme-x.y_z"} {
		if err := ValidSubjectTokens(good); err != nil {
			t.Errorf("type %q should map to a subject: %v", good, err)
		}
	}
	for _, bad := range []string{"", ".", "a.", ".a", "a..b", "a b", "a.*", "a.>", "a\x00b"} {
		if err := ValidSubjectTokens(bad); err == nil {
			t.Errorf("type %q must be rejected", bad)
		}
	}
}

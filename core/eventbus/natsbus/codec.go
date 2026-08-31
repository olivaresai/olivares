// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package natsbus

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/plugin"
	pb "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
)

// The bridge wire format is the FROZEN proto Event (sdk/plugin v1.proto,
// guarded by buf breaking): first-party observation payloads travel the typed
// oneof — never JSON — exactly as the S02 §3 contract words it for the plugin
// wire; a module-defined payload travels json_payload, the unversioned JSON
// fallback owned by the publishing and consuming modules.
//
// Why re-materialization matters: event.Event.Payload is `any`, and every typed
// reader (event.EdgeOf, event.ObservedTextOf, …) type-switches on the CONCRETE
// struct. A payload that crossed the wire as JSON and came back as
// map[string]any would make every first-party subscriber silently drop the
// event. The typed oneof guarantees the three observation payloads; the decoder
// registry re-materializes the known module-defined payloads; an UNREGISTERED
// type decodes to json.RawMessage — the one shape a tolerant consumer (the
// re-marshal pattern, e.g. modules/voice parseTelemetry) still understands.
//
// Time crosses as protobuf Timestamp: wall-clock lossless to the nanosecond,
// but the monotonic reading and the location are gone — it arrives UTC. No
// consumer compares event times with ==; documented here so none starts to.

// PayloadDecoder re-materializes one event type's JSON payload into its
// concrete Go type (value, not pointer — the typed readers accept both).
type PayloadDecoder func([]byte) (any, error)

// DefaultDecoders covers every module-defined event type whose payload struct
// lives in the SDK (importable from core). Composition roots extend the map via
// Options.Decoders for module-owned payload types (e.g. voice.Telemetry) —
// modules themselves are NOT importable from core (license boundary).
// Decoding is deliberately lenient (unknown JSON fields ignored) so a rolling
// upgrade with payload-field skew degrades to zero values, not to a dropped
// event; consumers already re-validate (e.g. DisallowUnknownFields consumers
// re-marshal first).
func DefaultDecoders() map[event.Type]PayloadDecoder {
	return map[event.Type]PayloadDecoder{
		event.TypeGuardrailObserved: func(b []byte) (any, error) {
			var v event.ObservedText
			err := json.Unmarshal(b, &v)
			return v, err
		},
		event.TypeApprovalRequested: func(b []byte) (any, error) {
			var v event.ApprovalRequest
			err := json.Unmarshal(b, &v)
			return v, err
		},
		event.TypePolicyChanged: func(b []byte) (any, error) {
			var v event.PolicyChange
			err := json.Unmarshal(b, &v)
			return v, err
		},
	}
}

// EncodeEvent marshals e for the bridge. First-party observation payloads use
// the typed oneof via the SAME exported converters the collector push plane
// uses (sdk/plugin.ObservationToPB — one wire codec, reviewed in one place);
// anything else is JSON in json_payload; a nil payload sets no oneof.
//
// It is exported so a distributed backend that carries some event types
// out-of-band (the enterprise durable JetStream bus) encodes them on the
// SAME frozen proto-Event wire as this Core-NATS bridge — one codec, reviewed
// once, and the event.ID travels for dedup either way.
func EncodeEvent(e event.Event) ([]byte, error) {
	pe := &pb.Event{
		Id:     e.ID,
		Type:   string(e.Type),
		Tenant: e.Tenant,
		Source: e.Source,
	}
	if !e.Time.IsZero() {
		pe.Time = timestamppb.New(e.Time)
	}
	switch p := e.Payload.(type) {
	case nil:
		// no payload, no oneof
	case model.Observation:
		obs, err := plugin.ObservationToPB(p)
		if err != nil {
			return nil, err
		}
		switch op := obs.GetPayload().(type) {
		case *pb.Observation_Edge:
			pe.Payload = &pb.Event_Edge{Edge: op.Edge}
		case *pb.Observation_Cost:
			pe.Payload = &pb.Event_Cost{Cost: op.Cost}
		case *pb.Observation_Finding:
			pe.Payload = &pb.Event_Finding{Finding: op.Finding}
		case *pb.Observation_Metric:
			pe.Payload = &pb.Event_Metric{Metric: op.Metric}
		default:
			return nil, fmt.Errorf("natsbus: observation payload %T has no event oneof mapping", op)
		}
	default:
		raw, err := json.Marshal(e.Payload)
		if err != nil {
			return nil, fmt.Errorf("natsbus: marshal %q payload: %w", e.Type, err)
		}
		pe.Payload = &pb.Event_JsonPayload{JsonPayload: raw}
	}
	return proto.Marshal(pe)
}

// DecodeEvent unmarshals a bridged event, re-materializing the payload: typed
// oneof → the sealed model.Observation; json_payload → the registered concrete
// type, or json.RawMessage when the type is unregistered. A decoder error is
// returned (the caller counts and drops — a malformed remote payload must not
// become per-event log noise at Warn).
//
// Exported alongside EncodeEvent so the enterprise durable backend
// decodes JetStream-delivered events through the SAME registry-driven
// re-materialization, with no payload-type drift between the two transports.
func DecodeEvent(data []byte, decoders map[event.Type]PayloadDecoder) (event.Event, error) {
	var pe pb.Event
	if err := proto.Unmarshal(data, &pe); err != nil {
		return event.Event{}, fmt.Errorf("natsbus: unmarshal event: %w", err)
	}
	e := event.Event{
		ID:     pe.GetId(),
		Type:   event.Type(pe.GetType()),
		Tenant: pe.GetTenant(),
		Source: pe.GetSource(),
	}
	if ts := pe.GetTime(); ts != nil {
		e.Time = ts.AsTime()
	}
	switch p := pe.GetPayload().(type) {
	case nil:
		// no payload
	case *pb.Event_Edge:
		obs, err := plugin.ObservationFromPB(&pb.Observation{Payload: &pb.Observation_Edge{Edge: p.Edge}})
		if err != nil {
			return event.Event{}, err
		}
		e.Payload = obs
	case *pb.Event_Cost:
		obs, err := plugin.ObservationFromPB(&pb.Observation{Payload: &pb.Observation_Cost{Cost: p.Cost}})
		if err != nil {
			return event.Event{}, err
		}
		e.Payload = obs
	case *pb.Event_Finding:
		obs, err := plugin.ObservationFromPB(&pb.Observation{Payload: &pb.Observation_Finding{Finding: p.Finding}})
		if err != nil {
			return event.Event{}, err
		}
		e.Payload = obs
	case *pb.Event_Metric:
		obs, err := plugin.ObservationFromPB(&pb.Observation{Payload: &pb.Observation_Metric{Metric: p.Metric}})
		if err != nil {
			return event.Event{}, err
		}
		e.Payload = obs
	case *pb.Event_JsonPayload:
		if dec, ok := decoders[e.Type]; ok {
			v, err := dec(p.JsonPayload)
			if err != nil {
				return event.Event{}, fmt.Errorf("natsbus: decode %q payload: %w", e.Type, err)
			}
			e.Payload = v
		} else {
			e.Payload = json.RawMessage(p.JsonPayload)
		}
	default:
		return event.Event{}, fmt.Errorf("natsbus: event %q carries an unrecognized payload %T", e.Type, p)
	}
	return e, nil
}

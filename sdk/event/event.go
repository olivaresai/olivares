// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package event is the event-bus vocabulary of Olivares AI: the Event envelope
// that flows from connectors through the engine to modules and output
// connectors, the Type discriminator, and the Handler a subscriber registers.
// It is licensed Apache-2.0 and depends only on the standard library and
// sibling sdk packages, so it crosses the connector boundary, the module
// boundary and the gRPC wire without dragging in the engine.
//
// An Event carries a Payload. For the first-party types the Payload is always a
// model.Observation and travels the typed gRPC oneof, never JSON; the invariant
// Type ⇒ concrete payload type is enforced by FromObservation and read back with
// the typed helpers (EdgeOf/CostOf/FindingOf). Modules may publish their own
// event Types with arbitrary payloads; those travel an unversioned JSON
// fallback on the wire that the publishing and consuming modules own.
package event

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// Type is the event discriminator. First-party types are namespaced by domain
// ("edge", "cost", "finding"); a module names its own as "<namespace>.<verb>".
// It is a plain string so a module can introduce its own without an SDK change.
type Type string

// Handler processes one delivered event. A non-nil error is logged by the
// engine and does not stop delivery to other subscribers; a handler that needs
// retry/dead-lettering implements it itself. A handler must be safe to call
// from a goroutine the bus owns and should honor ctx for cancellation.
type Handler func(ctx context.Context, e Event) error

// The first-party event types the engine publishes from connector observations.
const (
	// TypeEdgeObserved wraps a model.EdgeObservation.
	TypeEdgeObserved Type = "edge.observed"
	// TypeCostSampled wraps a model.CostSample.
	TypeCostSampled Type = "cost.sampled"
	// TypeFindingReported wraps a model.FindingReport.
	TypeFindingReported Type = "finding.reported"
	// TypeMetricSampled wraps a model.MetricSample.
	TypeMetricSampled Type = "metric.sampled"
)

// Event is the immutable envelope distributed on the bus. It carries the
// minimal-data fact (Payload), never raw payloads/secrets/PII (docs/SECURITY-HARDENING.md).
type Event struct {
	// ID is a unique id for the event (assigned by the engine; may be empty for
	// events constructed in-proc before publication).
	ID string
	// Type is the discriminator.
	Type Type
	// Tenant is the originating tenant as a string reference. The engine resolves
	// it to a model.TenantID; it is a string here so the SDK stays free of engine
	// id types.
	Tenant string
	// Source is the name of the component (connector/module) that emitted it.
	Source string
	// Time is when the underlying fact occurred (the connector's clock).
	Time time.Time
	// Payload is the fact. For the first-party Types it is a model.Observation;
	// a module-defined Type may carry any value (JSON-encoded on the wire).
	Payload any
}

// TypeForObservation returns the first-party event Type that wraps o. It is the
// single mapping from an observation kind to its event type, used by the engine
// when it lifts a connector observation onto the bus.
func TypeForObservation(o model.Observation) Type {
	switch o.ObservationType() {
	case model.ObsEdge:
		return TypeEdgeObserved
	case model.ObsCost:
		return TypeCostSampled
	case model.ObsFinding:
		return TypeFindingReported
	case model.ObsMetric:
		return TypeMetricSampled
	default:
		return ""
	}
}

// FromObservation builds an Event wrapping o, stamping the type, tenant, source
// and time. The engine uses it so the Type ⇒ payload invariant holds by
// construction.
func FromObservation(tenant, source string, o model.Observation) Event {
	e := Event{
		Type:    TypeForObservation(o),
		Tenant:  tenant,
		Source:  source,
		Payload: o,
	}
	// A connector may pass a DTO by value or by pointer (the DTOs use value
	// receivers, so a *EdgeObservation also satisfies Observation). Normalize the
	// payload to its value form so consumers reading via EdgeOf/CostOf/FindingOf
	// see one predictable type, and take the observation's own timestamp as the
	// event time.
	switch v := o.(type) {
	case model.EdgeObservation:
		e.Time = v.ObservedAt
	case *model.EdgeObservation:
		e.Payload, e.Time = *v, v.ObservedAt
	case model.CostSample:
		e.Time = v.OccurredAt
	case *model.CostSample:
		e.Payload, e.Time = *v, v.OccurredAt
	case model.FindingReport:
		e.Time = v.OccurredAt
	case *model.FindingReport:
		e.Payload, e.Time = *v, v.OccurredAt
	case model.MetricSample:
		e.Time = v.OccurredAt
	case *model.MetricSample:
		e.Payload, e.Time = *v, v.OccurredAt
	}
	return e
}

// EdgeOf returns the EdgeObservation payload of a TypeEdgeObserved event, or
// false if the event is not an edge event or its payload is malformed. It
// accepts a value or pointer payload (FromObservation normalizes to value, but a
// directly built event may carry either).
func EdgeOf(e Event) (model.EdgeObservation, bool) {
	if e.Type != TypeEdgeObserved {
		return model.EdgeObservation{}, false
	}
	switch o := e.Payload.(type) {
	case model.EdgeObservation:
		return o, true
	case *model.EdgeObservation:
		return *o, true
	default:
		return model.EdgeObservation{}, false
	}
}

// CostOf returns the CostSample payload of a TypeCostSampled event, or false.
func CostOf(e Event) (model.CostSample, bool) {
	if e.Type != TypeCostSampled {
		return model.CostSample{}, false
	}
	switch o := e.Payload.(type) {
	case model.CostSample:
		return o, true
	case *model.CostSample:
		return *o, true
	default:
		return model.CostSample{}, false
	}
}

// FindingOf returns the FindingReport payload of a TypeFindingReported event, or
// false.
func FindingOf(e Event) (model.FindingReport, bool) {
	if e.Type != TypeFindingReported {
		return model.FindingReport{}, false
	}
	switch o := e.Payload.(type) {
	case model.FindingReport:
		return o, true
	case *model.FindingReport:
		return *o, true
	default:
		return model.FindingReport{}, false
	}
}

// MetricOf returns the MetricSample payload of a TypeMetricSampled event, or false.
func MetricOf(e Event) (model.MetricSample, bool) {
	if e.Type != TypeMetricSampled {
		return model.MetricSample{}, false
	}
	switch o := e.Payload.(type) {
	case model.MetricSample:
		return o, true
	case *model.MetricSample:
		return *o, true
	default:
		return model.MetricSample{}, false
	}
}

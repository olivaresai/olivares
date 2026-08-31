// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package example is a reference SourceConnector. It emits synthetic edge
// observations so the SDK, the module runtime and the event bus can be exercised
// end-to-end, and so connector authors (sessions) have a minimal,
// correct model to copy. It imports only the SDK — never the engine — which is
// what keeps every connector under Apache-2.0 and free of the AGPL boundary.
package example

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.example-source"

// Source is the reference source connector. It emits a configurable number of
// edge observations against a configurable resource, then completes. A real
// connector would instead tail an audit log or receive OTLP, but the shape — a
// Descriptor, Open to read config, Gather to emit, Close to release — is the same.
type Source struct {
	count    int
	resource string
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a reference source with default configuration.
func New() *Source {
	return &Source{count: 1, resource: "demo.customers"}
}

// Descriptor returns the connector's self-description, declaring the two
// settings Open reads.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Example source",
		Description: "Emits synthetic read access edges; a worked example for connector authors.",
		ConfigFields: []sdk.ConfigField{
			{Key: "count", Type: sdk.FieldInt, Default: "1", Description: "how many edges to emit"},
			{Key: "resource", Type: sdk.FieldString, Default: "demo.customers", Description: "the resource the edges touch"},
		},
	}
}

// Open reads configuration. Defaults apply when a setting is absent.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.count = cfg.GetInt("count", s.count)
	if r := cfg.Get("resource"); r != "" {
		s.resource = r
	}
	return nil
}

// Gather emits count synthetic read-access edges and returns (a batch source).
// It honors ctx so the runtime can cancel a long emit mid-flight.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	for i := 0; i < s.count; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		obs := model.EdgeObservation{
			OriginKind:   "agent",
			OriginRef:    "claude-code",
			ResourceKind: "demo.table",
			ResourceRef:  s.resource,
			Mode:         model.ModeRead,
			Source:       model.SignalOTEL,
			Confidence:   model.ConfidenceApproximate,
			ToolRef:      "demo.query",
			ObservedAt:   time.Now().UTC(),
		}
		if err := sink.Emit(ctx, obs); err != nil {
			return err
		}
	}
	return nil
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

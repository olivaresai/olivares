// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk_test

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// helloSource is a minimal SourceConnector. A real connector talks to an external
// system in Gather and emits what it sees; the contract is identical here — the
// lifecycle (Descriptor → Open → Gather → Close) and the boundary (it imports only
// the SDK, never the engine) are exactly what every connector follows.
type helloSource struct{ path string }

func (helloSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       "example.hello",
		Version:    "0.1.0",
		APIVersion: sdk.APIVersion,
		Type:       sdk.TypeSource,
		Title:      "Hello source",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "the file the agent read"},
		},
	}
}

func (h *helloSource) Open(_ context.Context, cfg sdk.Config) error {
	// Validate configuration here, not in Gather (a config error fails fast).
	h.path = cfg.Get("path")
	if h.path == "" {
		return fmt.Errorf("example.hello: 'path' is required")
	}
	return nil
}

func (h *helloSource) Gather(ctx context.Context, sink sdk.Sink) error {
	// Emit one fact: an agent read a file. A streaming source blocks here until ctx
	// is done, emitting as facts arrive; a batch source emits and returns nil.
	return sink.Emit(ctx, model.EdgeObservation{
		OriginKind:   "agent",
		OriginRef:    "billing-agent",
		ResourceKind: "file",
		ResourceRef:  h.path,
		Mode:         model.ModeRead,
		Source:       model.SignalOTEL,
		Confidence:   model.ConfidenceAttributed,
	})
}

func (helloSource) Close(context.Context) error { return nil }

// collectSink is a trivial in-memory Sink. In production the engine provides the
// Sink and lifts each observation onto the event bus.
type collectSink struct{ edges []model.EdgeObservation }

func (s *collectSink) Emit(_ context.Context, obs model.Observation) error {
	if e, ok := obs.(model.EdgeObservation); ok {
		s.edges = append(s.edges, e)
	}
	return nil
}

// Example runs a source connector end to end: configure it, gather once into a
// sink, and inspect the emitted R/RW access edge.
func Example() {
	ctx := context.Background()

	var src sdk.SourceConnector = &helloSource{}
	if err := src.Open(ctx, sdk.Config{Settings: map[string]string{"path": "/repo/README.md"}}); err != nil {
		panic(err)
	}
	defer src.Close(ctx) //nolint:errcheck // example

	var sink collectSink
	if err := src.Gather(ctx, &sink); err != nil {
		panic(err)
	}

	e := sink.edges[0]
	fmt.Printf("%s %s %s %s\n", e.OriginRef, e.Mode, e.ResourceKind, e.ResourceRef)
	// Output: billing-agent read file /repo/README.md
}

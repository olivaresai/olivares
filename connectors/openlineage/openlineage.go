// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openlineage

import (
	"context"
	"errors"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/connectors/internal/logtail"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.openlineage"

// signalSource is this connector's SignalSource value. The SDK does not seed an
// OpenLineage signal, so the connector introduces its own (a SignalSource is an
// open string by design — docs/contracts/S02 §6 note on mysql_audit).
const signalSource = model.SignalSource("openlineage")

// resourceKind is the resource class every dataset edge carries.
const resourceKind = "openlineage.dataset"

// Source is the openlineage source connector. It tails a newline-delimited
// OpenLineage RunEvents file (written by the OpenLineage file transport) and
// emits one read edge per input dataset and one write edge per output dataset of
// each terminal COMPLETE event. The zero value is not usable; call New.
type Source struct {
	path   string
	follow bool
	shared identity.SharedSet
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an openlineage source with default configuration (follow on).
func New() *Source { return &Source{follow: true} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "OpenLineage",
		Description: "Enriches the R/RW map with observed data-flow lineage from an OpenLineage RunEvents file, read-only.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "path to the OpenLineage RunEvents file (newline-delimited JSON written by the file transport)"},
			{Key: "follow", Type: sdk.FieldBool, Default: "true", Description: "tail the events file continuously"},
			{Key: "shared_accounts", Type: sdk.FieldString, Description: "comma-separated job references (namespace/name) that are shared/pooled (attribution marked approximate)"},
		},
	}
}

// Open reads and validates configuration. A missing path is a configuration
// error reported here, not deferred to Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("openlineage: path is required")
	}
	s.follow = cfg.GetBool("follow", true)
	s.shared = identity.ParseSharedAccounts(cfg.Get("shared_accounts"))
	return nil
}

// Gather tails the configured RunEvents file, emitting the read/write edges of
// each terminal COMPLETE event. The file is newline-delimited JSON, so it
// supports continuous follow (follow=true) and batch read (follow=false).
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	return logtail.Tail(ctx, s.path, logtail.Options{Follow: s.follow}, func(line []byte) error {
		ev, ok := parseEvent(line)
		if !ok {
			return nil
		}
		return s.emit(ctx, sink, ev)
	})
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// emit builds and emits the edges for one RunEvent, skipping non-terminal events
// and events without a parseable timestamp or job reference.
func (s *Source) emit(ctx context.Context, sink sdk.Sink, ev runEvent) error {
	edges, ok := s.buildEdges(ev)
	if !ok {
		return nil
	}
	for _, e := range edges {
		if err := sink.Emit(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// buildEdges maps a RunEvent to its read/write edges. It returns ok=false (no
// edges) when the event is not a terminal COMPLETE, lacks a parseable eventTime,
// or lacks a job reference — only an actually-completed run with a known origin
// and a source clock yields edges. To avoid double-counting a START+COMPLETE
// pair, only COMPLETE (or an empty eventType) is emitted.
func (s *Source) buildEdges(ev runEvent) ([]model.EdgeObservation, bool) {
	if !isComplete(ev.EventType) {
		return nil, false
	}
	ts, ok := parseTime(ev.EventTime)
	if !ok {
		return nil, false
	}
	origin := jobRef(ev.Job)
	if origin == "" {
		return nil, false
	}
	conf := s.shared.ConfidenceFor(origin)

	edge := func(d dataset, mode model.AccessMode) (model.EdgeObservation, bool) {
		ref := datasetRef(d)
		if ref == "" {
			return model.EdgeObservation{}, false
		}
		return model.EdgeObservation{
			OriginKind:   identity.OriginKind,
			OriginRef:    origin,
			ResourceKind: resourceKind,
			ResourceRef:  ref,
			Mode:         mode,
			Source:       signalSource,
			Confidence:   conf,
			ToolRef:      ev.EventType,
			ObservedAt:   ts,
		}, true
	}

	var edges []model.EdgeObservation
	for _, in := range ev.Inputs {
		if e, ok := edge(in, model.ModeRead); ok {
			edges = append(edges, e)
		}
	}
	for _, out := range ev.Outputs {
		if e, ok := edge(out, model.ModeWrite); ok {
			edges = append(edges, e)
		}
	}
	if len(edges) == 0 {
		return nil, false
	}
	return edges, true
}

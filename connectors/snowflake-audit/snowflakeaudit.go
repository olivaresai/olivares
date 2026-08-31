// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package snowflakeaudit

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
const Name = "olivares.snowflake-audit"

// SignalSnowflakeAccessHistory is the SignalSource for this connector. The SDK
// seeds pg_audit and cloudtrail but not Snowflake; a connector introduces its
// own open-string source without an SDK release (docs/contracts/S02 §6).
const SignalSnowflakeAccessHistory model.SignalSource = "snowflake_access_history"

// Source is the snowflake-audit source connector. It reads an NDJSON file the
// operator exported from SNOWFLAKE.ACCOUNT_USAGE.ACCESS_HISTORY and emits one
// EdgeObservation per accessed object (or per accessed column when the row is
// column-grained). The zero value is not usable; call New.
type Source struct {
	path   string
	follow bool
	shared identity.SharedSet
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a snowflake-audit source with default configuration (follow off:
// ACCESS_HISTORY is re-polled by the engine scheduler as a batch export, with a
// latency of up to ~3h on Snowflake's side; tailing a static export is the
// exception, not the default).
func New() *Source { return &Source{follow: false} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Snowflake ACCESS_HISTORY",
		Description: "Captures column-level R/RW access from Snowflake ACCESS_HISTORY (NDJSON export), read-only.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "path to the NDJSON file exported from SNOWFLAKE.ACCOUNT_USAGE.ACCESS_HISTORY (one row per line)"},
			{Key: "follow", Type: sdk.FieldBool, Default: "false", Description: "tail the export continuously; default false (re-polled as a batch by the scheduler)"},
			{Key: "shared_accounts", Type: sdk.FieldString, Description: "comma-separated USER_NAME/ROLE_NAME values that are shared/pooled (attribution marked approximate)"},
		},
	}
}

// Open reads and validates configuration. A missing path is a configuration
// error reported here, not deferred to Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("snowflake-audit: path is required")
	}
	s.follow = cfg.GetBool("follow", false)
	s.shared = identity.ParseSharedAccounts(cfg.Get("shared_accounts"))
	return nil
}

// Gather reads the configured NDJSON export line by line, emitting an edge per
// accessed object/column. In batch mode it returns nil at EOF; with follow=true
// it tails the file until the context is canceled.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	return logtail.Tail(ctx, s.path, logtail.Options{Follow: s.follow}, func(line []byte) error {
		row, ok := parseRow(line)
		if !ok {
			return nil
		}
		return s.emit(ctx, sink, row)
	})
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// emit builds and emits every edge for one ACCESS_HISTORY row.
func (s *Source) emit(ctx context.Context, sink sdk.Sink, row accessRow) error {
	for _, e := range s.buildEdges(row) {
		if err := sink.Emit(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// buildEdges maps one ACCESS_HISTORY row to its edges: one per object (or per
// column when the object lists columns) across the three access buckets, with
// the mode taken verbatim from the bucket the object appears in.
func (s *Source) buildEdges(row accessRow) []model.EdgeObservation {
	ts, ok := parseTime(row.QueryStartTime)
	if !ok {
		return nil
	}
	ref := strings.TrimSpace(row.UserName)
	if ref == "" {
		return nil
	}
	// Confidence drops to approximate if EITHER the user or its role is a declared
	// shared account; the raw USER_NAME is always emitted (docs/contracts).
	// ROLE_NAME is not part of ACCESS_HISTORY itself; it is honored only when an
	// export joins it in, and an empty role never affects confidence.
	conf := s.shared.ConfidenceFor(row.UserName, row.RoleName)

	var edges []model.EdgeObservation
	add := func(objs []accessObject, mode model.AccessMode, tool string) {
		for _, obj := range objs {
			name := strings.TrimSpace(obj.ObjectName)
			if name == "" {
				continue
			}
			cols := nonEmptyColumns(obj.Columns)
			if len(cols) == 0 {
				edges = append(edges, model.EdgeObservation{
					OriginKind:   identity.OriginKind,
					OriginRef:    ref,
					ResourceKind: kindTable,
					ResourceRef:  name,
					Mode:         mode,
					Source:       SignalSnowflakeAccessHistory,
					Confidence:   conf,
					ToolRef:      tool,
					ObservedAt:   ts,
				})
				continue
			}
			for _, c := range cols {
				edges = append(edges, model.EdgeObservation{
					OriginKind:   identity.OriginKind,
					OriginRef:    ref,
					ResourceKind: kindColumn,
					ResourceRef:  name + "." + c,
					Mode:         mode,
					Source:       SignalSnowflakeAccessHistory,
					Confidence:   conf,
					ToolRef:      tool,
					ObservedAt:   ts,
				})
			}
		}
	}

	add(row.DirectObjectsAccessed, model.ModeRead, toolDirect)
	add(row.BaseObjectsAccessed, model.ModeRead, toolBase)
	add(row.ObjectsModified, model.ModeWrite, toolModified)
	return edges
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package databricksuc

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
const Name = "olivares.databricks-uc"

// SignalDatabricksUC is the SignalSource for this connector. The SDK seeds
// pg_audit and cloudtrail but not Databricks; a connector introduces its own
// open-string source without an SDK release (docs/contracts/S02 §6).
const SignalDatabricksUC model.SignalSource = "databricks_uc"

// Source is the databricks-uc source connector. It reads a newline-delimited
// JSON export of the Unity Catalog lineage system tables
// (system.access.table_lineage and system.access.column_lineage) and emits one
// EdgeObservation per R/RW side of each lineage row. The zero value is not
// usable; call New.
type Source struct {
	path   string
	follow bool
	shared identity.SharedSet
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a databricks-uc source with default configuration (batch; follow
// off — a lineage export is a periodically shipped snapshot, not a live tail).
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Databricks Unity Catalog lineage",
		Description: "Captures R/RW access to Unity Catalog tables/columns from the native lineage system tables (NDJSON export), read-only.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "path to the NDJSON export of system.access.table_lineage / column_lineage (one lineage row per line)"},
			{Key: "follow", Type: sdk.FieldBool, Default: "false", Description: "tail the export continuously; default false (a lineage export is a periodic batch snapshot)"},
			{Key: "shared_accounts", Type: sdk.FieldString, Description: "comma-separated created_by principals that are shared/pooled (attribution marked approximate)"},
		},
	}
}

// Open reads and validates configuration. A missing path is a configuration
// error reported here, not deferred to Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("databricks-uc: path is required")
	}
	s.follow = cfg.GetBool("follow", false)
	s.shared = identity.ParseSharedAccounts(cfg.Get("shared_accounts"))
	return nil
}

// Gather reads the configured NDJSON export and emits edges. Each line is one
// lineage row; a row contributes a read edge for its source and a write edge for
// its target. It batches when follow is false (the default for a shipped
// snapshot) and tails when follow is true.
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

// emit derives the read edge (source side) and write edge (target side) from a
// lineage row and emits each that is present.
func (s *Source) emit(ctx context.Context, sink sdk.Sink, row lineageRow) error {
	ts, ok := parseTime(row.EventTime)
	if !ok {
		return nil
	}
	ref := strings.TrimSpace(row.CreatedBy)
	if ref == "" {
		return nil
	}
	conf := s.shared.ConfidenceFor(ref)

	if kind, resource, ok := row.sourceRef(); ok {
		if err := sink.Emit(ctx, model.EdgeObservation{
			OriginKind:   identity.OriginKind,
			OriginRef:    ref,
			ResourceKind: kind,
			ResourceRef:  resource,
			Mode:         modeRead,
			Source:       SignalDatabricksUC,
			Confidence:   conf,
			ToolRef:      "",
			ObservedAt:   ts,
		}); err != nil {
			return err
		}
	}
	if kind, resource, ok := row.targetRef(); ok {
		if err := sink.Emit(ctx, model.EdgeObservation{
			OriginKind:   identity.OriginKind,
			OriginRef:    ref,
			ResourceKind: kind,
			ResourceRef:  resource,
			Mode:         modeWrite,
			Source:       SignalDatabricksUC,
			Confidence:   conf,
			ToolRef:      "",
			ObservedAt:   ts,
		}); err != nil {
			return err
		}
	}
	return nil
}

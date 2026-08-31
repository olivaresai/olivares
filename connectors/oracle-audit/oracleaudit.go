// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package oracleaudit

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
const Name = "olivares.oracle-audit"

// SignalOracleAudit is the SignalSource for this connector. The SDK seeds
// pg_audit and cloudtrail but not Oracle; a connector introduces its own
// open-string source without an SDK release (docs/contracts/S02 §6).
const SignalOracleAudit model.SignalSource = "oracle_audit"

// resourceKind is the resource class every emitted edge carries. The Unified
// Audit Trail attributes an action to a schema object named OBJECT_SCHEMA.OBJECT_NAME.
const resourceKind = "oracle.table"

// Source is the oracle-audit source connector. It reads an NDJSON export of the
// Oracle UNIFIED_AUDIT_TRAIL view (one row per line) and emits one
// EdgeObservation per audited data access. The zero value is not usable; call New.
type Source struct {
	path   string
	follow bool
	shared identity.SharedSet
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an oracle-audit source with default configuration (batch, no follow).
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Oracle Unified Audit Trail",
		Description: "Captures R/RW access from an NDJSON export of the Oracle UNIFIED_AUDIT_TRAIL view, read-only.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "path to the NDJSON export of UNIFIED_AUDIT_TRAIL (one audit row per line)"},
			{Key: "follow", Type: sdk.FieldBool, Default: "false", Description: "tail the export continuously (the operator appends new rows); default is a one-shot batch read"},
			{Key: "shared_accounts", Type: sdk.FieldString, Description: "comma-separated database users that are shared/pooled (attribution marked approximate)"},
		},
	}
}

// Open reads and validates configuration. A missing path is a configuration error
// reported here, not deferred to Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("oracle-audit: path is required")
	}
	s.follow = cfg.GetBool("follow", false)
	s.shared = identity.ParseSharedAccounts(cfg.Get("shared_accounts"))
	return nil
}

// Gather reads the configured NDJSON export line by line and emits an edge per
// audited access. With follow=false it returns nil at EOF (a one-shot batch over
// the exported rows); with follow=true it tails the file for appended rows until
// the context is canceled.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	return logtail.Tail(ctx, s.path, logtail.Options{Follow: s.follow}, func(line []byte) error {
		rec, ok := parseRow(line)
		if !ok {
			return nil
		}
		edge, ok := s.buildEdge(rec)
		if !ok {
			return nil
		}
		return sink.Emit(ctx, edge)
	})
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// buildEdge maps a Unified Audit Trail row to an EdgeObservation, or ok=false if
// the row is not an emittable data access (no object, an unparseable timestamp, or
// no identity). The mode is verbatim from ACTION_NAME and may be ModeUnknown — an
// unclassified action is emitted with an honest unknown mode, never dropped or
// guessed.
func (s *Source) buildEdge(rec row) (model.EdgeObservation, bool) {
	resource, ok := rec.resourceRef()
	if !ok {
		return model.EdgeObservation{}, false
	}
	ts, ok := parseTime(rec.eventTimestamp())
	if !ok {
		return model.EdgeObservation{}, false
	}
	user := strings.TrimSpace(rec.DBUsername)
	if user == "" {
		return model.EdgeObservation{}, false
	}

	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    user,
		ResourceKind: resourceKind,
		ResourceRef:  resource,
		Mode:         classifyAction(rec.ActionName),
		Source:       SignalOracleAudit,
		Confidence:   s.shared.ConfidenceFor(user),
		ToolRef:      strings.TrimSpace(rec.ActionName),
		ObservedAt:   ts,
	}, true
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bigqueryaudit

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
const Name = "olivares.bigquery-audit"

// SignalBigQueryAudit is the SignalSource for this connector. The SDK seeds
// pg_audit and cloudtrail but not BigQuery; a connector introduces its own
// open-string source without an SDK release (docs/contracts/S02 §6).
const SignalBigQueryAudit model.SignalSource = "bigquery_audit"

// Source is the bigquery-audit source connector. It reads Cloud Logging audit
// entries (NDJSON, one LogEntry per line) and emits one EdgeObservation per
// BigQuery table data access. The zero value is not usable; call New.
type Source struct {
	path   string
	follow bool
	shared identity.SharedSet
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a bigquery-audit source with default configuration (batch read).
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Google BigQuery audit",
		Description: "Captures R/RW access to BigQuery tables from the BigQueryAuditMetadata trail (Cloud Logging NDJSON export), read-only.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "path to the Cloud Logging audit export (NDJSON, one LogEntry per line) the connector reads"},
			{Key: "follow", Type: sdk.FieldBool, Default: "false", Description: "tail the export continuously; false reads the current file as a batch"},
			{Key: "shared_accounts", Type: sdk.FieldString, Description: "comma-separated principalEmails that are shared/pooled service accounts (attribution marked approximate)"},
		},
	}
}

// Open reads and validates configuration. A missing path is a configuration
// error reported here, not deferred to Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("bigquery-audit: path is required")
	}
	s.follow = cfg.GetBool("follow", false)
	s.shared = identity.ParseSharedAccounts(cfg.Get("shared_accounts"))
	return nil
}

// Gather reads the configured NDJSON export and emits an edge per table data
// access. The export is line-delimited (one LogEntry per line), so it uses
// logtail with the configured follow mode: batch (returns nil at EOF) when
// follow is false, continuous tail otherwise.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	return logtail.Tail(ctx, s.path, logtail.Options{Follow: s.follow}, func(line []byte) error {
		entry, ok := entryFromJSON(line)
		if !ok {
			return nil
		}
		edge, ok := s.buildEdge(entry)
		if !ok {
			return nil
		}
		return sink.Emit(ctx, edge)
	})
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// buildEdge maps a Cloud Logging audit entry to an EdgeObservation, or ok=false
// if the entry is not an emittable BigQuery table data access (no table-data
// event in the metadata, an unparseable timestamp, a non-table resourceName, or
// no principal identity).
func (s *Source) buildEdge(e logEntry) (model.EdgeObservation, bool) {
	pp := e.ProtoPayload

	mode, ok := classifyMode(pp.Metadata)
	if !ok {
		return model.EdgeObservation{}, false
	}
	ts, ok := parseTime(e.Timestamp)
	if !ok {
		return model.EdgeObservation{}, false
	}
	ref, ok := resourceRefFromName(pp.ResourceName)
	if !ok {
		return model.EdgeObservation{}, false
	}
	principal := strings.TrimSpace(pp.AuthenticationInfo.PrincipalEmail)
	if principal == "" {
		return model.EdgeObservation{}, false
	}

	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    principal,
		ResourceKind: "bigquery.table",
		ResourceRef:  ref,
		Mode:         mode,
		Source:       SignalBigQueryAudit,
		Confidence:   s.shared.ConfidenceFor(principal),
		ToolRef:      pp.MethodName,
		ObservedAt:   ts,
	}, true
}

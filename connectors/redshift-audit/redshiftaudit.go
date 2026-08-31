// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package redshiftaudit

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
const Name = "olivares.redshift-audit"

// SignalRedshiftAudit is the SignalSource for this connector. The SDK seeds
// pg_audit and cloudtrail but not Redshift; a connector introduces its own
// open-string source without an SDK release (docs/contracts/S02 §6).
const SignalRedshiftAudit model.SignalSource = "redshift_audit"

// resourceKind is the degraded, database-level resource granularity this
// connector emits. The user-activity log identifies the database (`db=`) but not
// the table; deriving a table by regex would be guessing (ARCHITECTURE.md).
const resourceKind = "redshift.database"

// Source is the redshift-audit source connector. It reads the Amazon Redshift
// user-activity audit log (exported by the operator) and emits one
// EdgeObservation per statement, classified by leading verb. The zero value is
// not usable; call New.
type Source struct {
	path   string
	follow bool
	shared identity.SharedSet
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a redshift-audit source with default configuration (batch read).
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Amazon Redshift user-activity audit",
		Description: "Captures R/RW access from the Amazon Redshift user-activity audit log (by-verb, database granularity), read-only.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "path to the exported Redshift user-activity log file to read"},
			{Key: "follow", Type: sdk.FieldBool, Default: "false", Description: "tail the log continuously; false reads the current file as a batch"},
			{Key: "shared_accounts", Type: sdk.FieldString, Description: "comma-separated database users that are shared/pooled (attribution marked approximate)"},
		},
	}
}

// Open reads and validates configuration. A missing path is a configuration
// error reported here, not deferred to Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("redshift-audit: path is required")
	}
	s.follow = cfg.GetBool("follow", false)
	s.shared = identity.ParseSharedAccounts(cfg.Get("shared_accounts"))
	return nil
}

// Gather reads the configured user-activity log line by line and emits an edge
// per statement. With follow=false it reads to EOF and returns nil (batch); with
// follow=true it tails the file until the context is canceled.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	return logtail.Tail(ctx, s.path, logtail.Options{Follow: s.follow}, func(line []byte) error {
		rec, ok := parseLine(string(line))
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

// buildEdge maps a parsed activity record to an EdgeObservation, or ok=false if
// it is not an emittable access (no user, no database, or an unparseable
// timestamp). Mode is the by-verb classification (ModeUnknown where Redshift's
// log gives no verb to classify); the SQL body is already discarded by parseLine.
func (s *Source) buildEdge(rec activityRecord) (model.EdgeObservation, bool) {
	if rec.user == "" || rec.db == "" {
		return model.EdgeObservation{}, false
	}
	ts, ok := parseTime(rec.timestamp)
	if !ok {
		return model.EdgeObservation{}, false
	}
	mode, verb := classifyVerb(rec.verb)
	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    rec.user,
		ResourceKind: resourceKind,
		ResourceRef:  rec.db,
		Mode:         mode,
		Source:       SignalRedshiftAudit,
		Confidence:   s.shared.ConfidenceFor(rec.user),
		ToolRef:      verb,
		ObservedAt:   ts,
	}, true
}

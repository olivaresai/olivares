// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mssqlaudit

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
const Name = "olivares.mssql-audit"

// Source is the mssql-audit source connector. It tails the operator's exported
// SQL Server Audit trail (newline-delimited JSON rows from sys.fn_get_audit_file)
// and emits one EdgeObservation per audited data access. The zero value is not
// usable; call New.
type Source struct {
	path   string
	follow bool
	shared identity.SharedSet
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an mssql-audit source with default configuration (follow on).
func New() *Source { return &Source{follow: true} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Microsoft SQL Server Audit",
		Description: "Captures R/RW access from the SQL Server Audit trail (NDJSON export of sys.fn_get_audit_file), read-only.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "path to the exported SQL Server Audit NDJSON file to read/tail"},
			{Key: "follow", Type: sdk.FieldBool, Default: "true", Description: "tail the audit export continuously (the export is line-delimited)"},
			{Key: "shared_accounts", Type: sdk.FieldString, Description: "comma-separated logins/users that are shared/pooled (attribution marked approximate)"},
		},
	}
}

// Open reads and validates configuration. A missing path is a configuration error
// reported here, not deferred to Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("mssql-audit: path is required")
	}
	s.follow = cfg.GetBool("follow", true)
	s.shared = identity.ParseSharedAccounts(cfg.Get("shared_accounts"))
	return nil
}

// Gather reads the exported audit trail (newline-delimited JSON) and emits an edge
// per audited data access. With follow=true it tails the file continuously; with
// follow=false it reads to EOF and returns nil.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	return logtail.Tail(ctx, s.path, logtail.Options{Follow: s.follow}, func(line []byte) error {
		rec, ok := recordFromLine(line)
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

// buildEdge maps a SQL Server Audit record to an EdgeObservation, or ok=false if
// the record is not an emittable data access (a non-DML/EXECUTE action, no
// resolvable object, no identity, or an unparseable timestamp).
func (s *Source) buildEdge(rec record) (model.EdgeObservation, bool) {
	mode, tool, ok := classifyAction(rec.ActionID, rec.ActionName)
	if !ok {
		return model.EdgeObservation{}, false
	}
	ref := resourceRef(rec.DatabaseName, rec.SchemaName, rec.ObjectName)
	if ref == "" {
		return model.EdgeObservation{}, false
	}
	ts, ok := parseTime(rec.EventTime)
	if !ok {
		return model.EdgeObservation{}, false
	}

	// Identity: the server principal (login) is the most distinctive reference;
	// fall back to the database user when the audit did not record a login. The
	// raw identity is always emitted; confidence drops to approximate if EITHER
	// the login or the database user is a declared shared account
	// (docs/contracts).
	origin := strings.TrimSpace(rec.ServerPrincipalName)
	if origin == "" {
		origin = strings.TrimSpace(rec.DatabasePrincipalName)
	}
	if origin == "" {
		return model.EdgeObservation{}, false
	}
	conf := s.shared.ConfidenceFor(rec.ServerPrincipalName, rec.DatabasePrincipalName)

	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    origin,
		ResourceKind: resourceKindFor(rec.ClassType),
		ResourceRef:  ref,
		Mode:         mode,
		Source:       SignalMSSQLAudit,
		Confidence:   conf,
		ToolRef:      tool,
		ObservedAt:   ts,
	}, true
}

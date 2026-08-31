// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mongoaudit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/connectors/internal/logtail"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.mongo-audit"

// SignalMongoAudit is the SignalSource for this connector. The SDK seeds pg_audit
// and cloudtrail but not MongoDB; a connector introduces its own open-string
// source without an SDK release (docs/contracts/S02 §6).
const SignalMongoAudit model.SignalSource = "mongo_audit"

// Source is the mongo-audit source connector. It tails a MongoDB JSON audit log
// and emits one EdgeObservation per authorized authCheck. The zero value is not
// usable; call New.
type Source struct {
	path   string
	follow bool
	shared identity.SharedSet
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a mongo-audit source with default configuration (follow on).
func New() *Source { return &Source{follow: true} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "MongoDB audit (authCheck)",
		Description: "Captures per-collection R/RW access from the MongoDB JSON audit log (authCheck), read-only.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "path to the MongoDB JSON audit log file to read/tail (auditLog.destination=file, format=JSON)"},
			{Key: "follow", Type: sdk.FieldBool, Default: "true", Description: "tail the audit log continuously"},
			{Key: "shared_accounts", Type: sdk.FieldString, Description: "comma-separated user or user@db identities that are shared/pooled (attribution marked approximate)"},
		},
	}
}

// Open reads and validates configuration. A missing path is a configuration error
// reported here, not deferred to Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("mongo-audit: path is required")
	}
	s.follow = cfg.GetBool("follow", true)
	s.shared = identity.ParseSharedAccounts(cfg.Get("shared_accounts"))
	return nil
}

// Gather tails the MongoDB JSON audit log (one JSON object per line) and emits an
// edge per authorized authCheck access. In batch mode it returns nil at EOF; in
// follow mode it blocks until the context is canceled.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	return logtail.Tail(ctx, s.path, logtail.Options{Follow: s.follow}, func(line []byte) error {
		edge, ok := s.buildEdge(line)
		if !ok {
			return nil
		}
		return sink.Emit(ctx, edge)
	})
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// buildEdge maps a MongoDB audit log line to an EdgeObservation, or ok=false if
// the line is not an emittable authorized data access: not valid JSON, not an
// authCheck, a denied check (result != 0), an unparseable timestamp, or no acting
// identity. The command body (param.args) is never read (it is not even declared
// on auditLine) — only the access edge is emitted (docs/SECURITY-HARDENING.md).
func (s *Source) buildEdge(line []byte) (model.EdgeObservation, bool) {
	var rec auditLine
	if err := json.Unmarshal(line, &rec); err != nil {
		return model.EdgeObservation{}, false
	}
	// Only authCheck carries the operation+namespace+identity; only an AUTHORIZED
	// check (result==0) is an access. A denied check (non-zero result, e.g. 13
	// Unauthorized) means the operation did NOT touch the resource — it is an
	// attempt, not an access, so it is not emitted as an edge.
	if rec.AType != atypeAuthCheck || rec.Result != resultAuthorized {
		return model.EdgeObservation{}, false
	}

	ref := originRef(rec.Users)
	if ref == "" {
		return model.EdgeObservation{}, false
	}
	ts, ok := parseTime(rec.TS.Date)
	if !ok {
		return model.EdgeObservation{}, false
	}
	kind, resource := resourceFor(rec.Param.NS)

	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    ref,
		ResourceKind: kind,
		ResourceRef:  resource,
		Mode:         commandToMode(rec.Param.Command),
		Source:       SignalMongoAudit,
		Confidence:   s.shared.ConfidenceFor(rec.Users[0].User, ref),
		ToolRef:      rec.Param.Command,
		ObservedAt:   ts,
	}, true
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package deltasharing

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
const Name = "olivares.delta-sharing"

// SignalDeltaSharing is the SignalSource for this connector. The SDK seeds
// pg_audit and cloudtrail but not Delta Sharing; a connector introduces its own
// open-string source without an SDK release (docs/contracts/S02 §6).
const SignalDeltaSharing model.SignalSource = "delta_sharing"

// Resource kinds emitted by this connector.
const (
	resourceKindTable = "deltasharing.table" // a specific shared table
	resourceKindShare = "deltasharing.share" // a share (share-level action)
)

// Source is the delta-sharing source connector. It reads a Delta Sharing server
// audit log (one JSON object per line) and emits one EdgeObservation per
// recipient read — the cross-org egress edge. The zero value is not usable; call
// New.
type Source struct {
	path   string
	follow bool
	shared identity.SharedSet
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a delta-sharing source with default configuration (follow on: the
// audit log is an append-only, line-delimited stream).
func New() *Source { return &Source{follow: true} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Delta Sharing (cross-org egress)",
		Description: "Captures cross-org data egress to recipients from a Delta Sharing server audit log, read-only.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "path to the Delta Sharing server audit log file (one JSON object per line)"},
			{Key: "follow", Type: sdk.FieldBool, Default: "true", Description: "tail the audit log continuously"},
			{Key: "shared_accounts", Type: sdk.FieldString, Description: "comma-separated recipient identities that are shared/pooled (attribution marked approximate)"},
		},
	}
}

// Open reads and validates configuration. A missing path is a configuration
// error reported here, not deferred to Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("delta-sharing: path is required")
	}
	s.follow = cfg.GetBool("follow", true)
	s.shared = identity.ParseSharedAccounts(cfg.Get("shared_accounts"))
	return nil
}

// Gather reads the configured audit log and emits an edge per recipient access.
// The log is line-delimited JSON; with follow set it tails continuously, with
// follow false it reads to EOF and returns nil.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	return logtail.Tail(ctx, s.path, logtail.Options{Follow: s.follow}, func(line []byte) error {
		e, ok := parseEntry(line)
		if !ok {
			return nil
		}
		edge, ok := s.buildEdge(e)
		if !ok {
			return nil
		}
		return sink.Emit(ctx, edge)
	})
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// buildEdge maps an audit entry to an EdgeObservation, or ok=false if it is not
// an emittable recipient access (no recipient identity, no resolvable resource,
// or an unparseable timestamp).
//
// The recipient is a cross-org identity, so OriginKind is always "identity" and
// the recipient is the OriginRef (the raw identity is always emitted). A
// recipient declared shared/pooled in shared_accounts drops to approximate so
// module VI resolves the identity↔org attribution; the read mode is verbatim from
// the action (docs/contracts/§6).
func (s *Source) buildEdge(e entry) (model.EdgeObservation, bool) {
	if e.Recipient == "" {
		return model.EdgeObservation{}, false
	}
	ts, ok := parseTime(e.Timestamp)
	if !ok {
		return model.EdgeObservation{}, false
	}
	kind, ref, ok := resolveResource(e)
	if !ok {
		return model.EdgeObservation{}, false
	}
	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    e.Recipient,
		ResourceKind: kind,
		ResourceRef:  ref,
		Mode:         classifyAction(e.Action),
		Source:       SignalDeltaSharing,
		Confidence:   s.shared.ConfidenceFor(e.Recipient),
		ToolRef:      e.Action,
		ObservedAt:   ts,
	}, true
}

// resolveResource returns the resource kind and reference for an audit entry. A
// table-scoped action (carrying a table) is a shared table, referenced as
// share.schema.table; a share-level action (e.g. listShares) is the share
// itself. ok=false if there is no share to anchor the resource on.
func resolveResource(e entry) (kind, ref string, ok bool) {
	if e.Share == "" {
		return "", "", false
	}
	if isTableScoped(e) {
		return resourceKindTable, e.Share + "." + e.Schema + "." + e.Table, true
	}
	return resourceKindShare, e.Share, true
}

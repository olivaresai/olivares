// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureblobaudit

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
const Name = "olivares.azure-blob-audit"

// SignalAzureBlobAudit is the SignalSource for this connector. The SDK seeds
// pg_audit and cloudtrail but not Azure Blob; a connector introduces its own
// open-string source without an SDK release (docs/contracts/S02 §6).
const SignalAzureBlobAudit model.SignalSource = "azureblob_audit"

// Source is the azure-blob-audit source connector. It tails an exported Azure
// StorageBlobLogs file (line-delimited JSON) and emits one EdgeObservation per
// blob access. The zero value is not usable; call New.
type Source struct {
	path   string
	follow bool
	shared identity.SharedSet
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an azure-blob-audit source with default configuration (follow on).
func New() *Source { return &Source{follow: true} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Azure Blob Storage (StorageBlobLogs)",
		Description: "Captures R/RW access to Azure Blob Storage from exported StorageBlobLogs diagnostic logs, read-only.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "path to the exported StorageBlobLogs file (line-delimited JSON) to read/tail"},
			{Key: "follow", Type: sdk.FieldBool, Default: "true", Description: "tail the log continuously as new lines are appended"},
			{Key: "shared_accounts", Type: sdk.FieldString, Description: "comma-separated requester app IDs (or auth types) that are shared/pooled (attribution marked approximate)"},
		},
	}
}

// Open reads and validates configuration. A missing path is a configuration
// error reported here, not deferred to Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("azure-blob-audit: path is required")
	}
	s.follow = cfg.GetBool("follow", true)
	s.shared = identity.ParseSharedAccounts(cfg.Get("shared_accounts"))
	return nil
}

// Gather tails the configured log, emitting an edge per StorageBlobLogs line.
// The export is line-delimited JSON, so it supports continuous follow.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	return logtail.Tail(ctx, s.path, logtail.Options{Follow: s.follow}, func(line []byte) error {
		rec, ok := recordFromJSON(line)
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

// buildEdge maps a StorageBlobLogs record to an EdgeObservation, or ok=false if
// the record is not an emittable blob access (no resolvable resource, no
// identity, or an unparseable timestamp).
func (s *Source) buildEdge(rec record) (model.EdgeObservation, bool) {
	ts, ok := parseTime(rec.Time)
	if !ok {
		return model.EdgeObservation{}, false
	}
	kind, ref, ok := resolveResource(rec.URI)
	if !ok {
		return model.EdgeObservation{}, false
	}

	// Identity: the AAD application / service principal is the most specific
	// raw reference (the per-agent bridge); fall back to the authentication type
	// when no OAuth requester is present (e.g. AccountKey/SAS/Anonymous). The raw
	// identity is always emitted; confidence drops to approximate only when the
	// reference is a declared shared account (docs/contracts).
	originRef := firstNonEmpty(rec.requesterAppID(), rec.authType())
	if originRef == "" {
		return model.EdgeObservation{}, false
	}
	conf := s.shared.ConfidenceFor(originRef)

	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    originRef,
		ResourceKind: kind,
		ResourceRef:  ref,
		Mode:         classifyMode(rec.OperationName, rec.Category),
		Source:       SignalAzureBlobAudit,
		Confidence:   conf,
		ToolRef:      rec.OperationName,
		ObservedAt:   ts,
	}, true
}

// firstNonEmpty returns the first non-empty argument, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

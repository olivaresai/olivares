// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gcsaudit

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
const Name = "olivares.gcs-audit"

// signalGCSAudit is the SignalSource this connector stamps on every edge. The SDK
// seeds pg_audit and cloudtrail but not GCS; a connector introduces its own open
// SignalSource value without an SDK release (docs/contracts/S02 §6 note).
const signalGCSAudit = model.SignalSource("gcs_audit")

// Source is the gcs-audit source connector. It reads an operator-exported Cloud
// Audit Logs file (NDJSON Cloud Logging entries for storage.googleapis.com) and
// emits one EdgeObservation per Cloud Storage access. The zero value is not
// usable; call New.
type Source struct {
	path   string
	follow bool
	shared identity.SharedSet
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a gcs-audit source with default configuration (batch, follow off).
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Google Cloud Storage Audit",
		Description: "Captures R/RW access to Cloud Storage from exported Cloud Audit Logs (NDJSON), read-only.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "path to the exported Cloud Audit Logs file (NDJSON Cloud Logging entries)"},
			{Key: "follow", Type: sdk.FieldBool, Default: "false", Description: "tail the file continuously (default false: a batched export)"},
			{Key: "shared_accounts", Type: sdk.FieldString, Description: "comma-separated principal emails that are shared/pooled (attribution marked approximate)"},
		},
	}
}

// Open reads and validates configuration. A missing path is a configuration error
// reported here, not deferred to Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("gcs-audit: path is required")
	}
	s.follow = cfg.GetBool("follow", false)
	s.shared = identity.ParseSharedAccounts(cfg.Get("shared_accounts"))
	return nil
}

// Gather reads the configured NDJSON file line by line and emits an edge per
// Cloud Storage access. With follow=false it reads to EOF and returns nil; with
// follow=true it tails the file until the context is canceled (logtail handles
// both, read-only).
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	return logtail.Tail(ctx, s.path, logtail.Options{Follow: s.follow}, func(line []byte) error {
		e, ok := entryFromLine(line)
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

// buildEdge maps a Cloud Logging entry to an EdgeObservation, or ok=false if it is
// not an emittable Cloud Storage access (a non-storage service, no resolvable
// resource, no principal, or an unparseable timestamp). The mode comes verbatim
// from methodName; an unmapped method is emitted with ModeUnknown (it IS a Cloud
// Storage access, the read/write nature is just not classified).
func (s *Source) buildEdge(e entry) (model.EdgeObservation, bool) {
	if e.ProtoPayload.ServiceName != gcsServiceName {
		return model.EdgeObservation{}, false
	}
	ts, ok := parseTime(e.Timestamp)
	if !ok {
		return model.EdgeObservation{}, false
	}
	kind, ref, ok := resolveResource(e.ProtoPayload.ResourceName)
	if !ok {
		return model.EdgeObservation{}, false
	}
	principal := strings.TrimSpace(e.ProtoPayload.AuthenticationInfo.PrincipalEmail)
	if principal == "" {
		return model.EdgeObservation{}, false
	}
	return model.EdgeObservation{
		OriginKind:   originKind,
		OriginRef:    principal,
		ResourceKind: kind,
		ResourceRef:  ref,
		Mode:         classifyMethod(e.ProtoPayload.MethodName),
		Source:       signalGCSAudit,
		Confidence:   s.shared.ConfidenceFor(principal),
		ToolRef:      e.ProtoPayload.MethodName,
		ObservedAt:   ts,
	}, true
}

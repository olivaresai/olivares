// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package icebergcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.iceberg-catalog"

// maxSnapshotBytes caps the snapshot read so a corrupt or hostile export cannot
// exhaust memory; a real catalog grant export is small.
const maxSnapshotBytes = 64 << 20 // 64 MiB

// Source is the iceberg-catalog source connector. It reads a single JSON snapshot
// the operator exported from the catalog and emits one PERMITTED policy edge per
// table-data grant (and per vended-credential privilege). It is a BATCH source:
// Gather reads the snapshot once and returns; the engine re-polls on the operator's
// schedule. The zero value is not usable; call New.
type Source struct {
	path   string
	follow bool
	shared identity.SharedSet
}

// Compile-time proof that Source satisfies the SDK contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an iceberg-catalog source with default configuration (follow off:
// the snapshot is a single object read in one batch, not a growing log).
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Apache Iceberg REST Catalog / Polaris",
		Description: "Reads an operator-exported catalog grant snapshot and emits PERMITTED table R/RW grants and vended-credential principals (policy side), read-only.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "path to the exported catalog snapshot JSON file"},
			{Key: "follow", Type: sdk.FieldBool, Default: "false", Description: "unused: the snapshot is a single JSON object read as a batch (a grant snapshot is not a growing log)"},
			{Key: "shared_accounts", Type: sdk.FieldString, Description: "comma-separated principals that are shared/pooled (grant attribution marked approximate)"},
		},
	}
}

// Open reads and validates configuration. A missing path is a configuration error
// reported here, not deferred to Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("iceberg-catalog: path is required")
	}
	s.follow = cfg.GetBool("follow", false)
	s.shared = identity.ParseSharedAccounts(cfg.Get("shared_accounts"))
	return nil
}

// Gather reads the snapshot JSON object and emits one PERMITTED policy edge per
// table-data grant and per vended-credential privilege. The snapshot is a single
// JSON object (not line-delimited), so it is read whole with os.ReadFile +
// json.Unmarshal. It is a batch read: it returns nil once the snapshot is emitted.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	data, err := readFileCapped(s.path)
	if err != nil {
		return err
	}
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("iceberg-catalog: parse snapshot %s: %w", s.path, err)
	}

	edges, ok := s.buildEdges(snap)
	if !ok {
		// An unparseable snapshot timestamp would shift every edge's natural-key
		// timestamp, so the whole snapshot is skipped rather than emitted with a
		// fabricated time (ObservedAt must be the source clock —).
		return nil
	}
	for _, e := range edges {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := sink.Emit(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// Close releases resources; this connector holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// buildEdges maps a snapshot to its PERMITTED policy edges. It returns ok=false if
// the snapshot timestamp cannot be parsed (every edge would carry a corrupt
// ObservedAt). Static grants are attributed (approximate if the principal is a
// declared shared account); each vended credential's principal is ephemeral, so
// every vended edge is approximate (ambiguous attribution) regardless of config.
func (s *Source) buildEdges(snap snapshot) ([]model.EdgeObservation, bool) {
	ts, ok := parseTime(snap.SnapshotAt)
	if !ok {
		return nil, false
	}

	var edges []model.EdgeObservation

	// Static grants: the durable permitted side.
	for _, g := range snap.Grants {
		mode, emit := privilegeToMode(g.Privilege)
		if !emit || g.Principal == "" || g.Table == "" {
			continue
		}
		edges = append(edges, model.EdgeObservation{
			OriginKind:   identity.OriginKind,
			OriginRef:    g.Principal,
			ResourceKind: resourceKind,
			ResourceRef:  g.Table,
			Mode:         mode,
			Source:       model.SignalPolicy,
			Confidence:   s.shared.ConfidenceFor(g.Principal),
			ToolRef:      strings.TrimSpace(g.Privilege),
			ObservedAt:   ts,
		})
	}

	// Vended credentials: each is a discovered NHI principal; one edge per
	// privilege. A vended/ephemeral credential is ambiguous attribution, so the
	// confidence is ALWAYS approximate (not subject to shared_accounts).
	for _, v := range snap.VendedCredentials {
		if v.Principal == "" || v.Table == "" {
			continue
		}
		for _, priv := range v.Privileges {
			mode, emit := privilegeToMode(priv)
			if !emit {
				continue
			}
			edges = append(edges, model.EdgeObservation{
				OriginKind:   identity.OriginKind,
				OriginRef:    v.Principal,
				ResourceKind: resourceKind,
				ResourceRef:  v.Table,
				Mode:         mode,
				Source:       model.SignalPolicy,
				Confidence:   model.ConfidenceApproximate,
				ToolRef:      strings.TrimSpace(priv),
				ObservedAt:   ts,
			})
		}
	}

	return edges, true
}

// readFileCapped reads up to maxSnapshotBytes from path, read-only. A snapshot
// larger than the cap is an error rather than a silent truncation that would
// json-fail confusingly.
func readFileCapped(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied snapshot path, read-only
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxSnapshotBytes {
		return nil, fmt.Errorf("iceberg-catalog: snapshot %s exceeds %d bytes", path, maxSnapshotBytes)
	}
	return data, nil
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azurekeyvault

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.azure-key-vault"

// signalAzureDiag is the SignalSource stamped on every edge (an open string, S02 §6).
const signalAzureDiag = model.SignalSource("azure_diagnostic")

// Source is the Azure Key Vault / Managed HSM audit connector. It satisfies
// sdk.SourceConnector (OBSERVED key/secret-access edges) and
// identitysource.GraphProvider (the secret_store inventory). Call New.
type Source struct {
	path string
	now  func() time.Time
}

var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns an azure-key-vault source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Azure Key Vault & Managed HSM (AuditEvent)",
		Description: "Observes Azure Key Vault and Managed HSM use from diagnostic AuditEvent logs (who used which key/secret), read-only. Never reads a secret value or key material.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "AuditEvent export file, or a directory of *.json / *.json.gz (records[] wrapper or NDJSON)."},
		},
	}
}

// Open reads and validates configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("azure-key-vault: path is required")
	}
	return nil
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// Gather reads the configured export and emits one edge per AuditEvent that names a
// governed key/secret/cert/vault object.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	files, err := s.listFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		recs, err := readRecords(f)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			if err := ctx.Err(); err != nil {
				return err
			}
			edge, ok := s.buildEdge(rec)
			if !ok {
				continue
			}
			if err := sink.Emit(ctx, edge); err != nil {
				return err
			}
		}
	}
	return nil
}

// Snapshot exposes the vaults / Managed HSMs seen in the export as secret_store
// NHIs. With no configured path it returns an empty graph.
func (s *Source) Snapshot(_ context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceAzureKeyVault, CapturedAt: s.clock().UTC()}
	if s.path == "" {
		return g, nil
	}
	files, err := s.listFiles()
	if err != nil {
		return identitysource.Graph{}, err
	}
	stores := map[string]store{}
	for _, f := range files {
		recs, err := readRecords(f)
		if err != nil {
			return identitysource.Graph{}, err
		}
		for _, rec := range recs {
			if st, ok := storeFromResourceID(rec.resourceID()); ok {
				stores[st.ref()] = st
			}
		}
	}
	refs := make([]string, 0, len(stores))
	for ref := range stores {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		st := stores[ref]
		g.Identities = append(g.Identities, identitysource.Identity{
			Ref:         ref,
			Type:        identitysource.PrincipalNHI,
			Kind:        identitysource.KindSecretStore,
			DisplayName: st.displayName(),
			Source:      identitysource.SourceAzureKeyVault,
			Attributes:  map[string]string{"provider": "azure", "service": st.service, "name": st.name},
		})
	}
	return g, nil
}

// buildEdge maps one AuditEvent record to an edge, or ok=false for a non-AuditEvent
// category, an operation naming no governed object (Authentication), no caller, or
// an unparseable timestamp.
func (s *Source) buildEdge(r record) (model.EdgeObservation, bool) {
	if r.Category != "" && r.Category != categoryAuditEvent {
		return model.EdgeObservation{}, false
	}
	kind, mode, ok := classify(r)
	if !ok {
		return model.EdgeObservation{}, false
	}
	caller := r.caller()
	if caller == "" {
		return model.EdgeObservation{}, false
	}
	ref := r.objectRef()
	if ref == "" {
		return model.EdgeObservation{}, false
	}
	ts, ok := parseTime(r.Time)
	if !ok {
		return model.EdgeObservation{}, false
	}
	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    caller,
		ResourceKind: kind,
		ResourceRef:  ref,
		Mode:         mode,
		Source:       signalAzureDiag,
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      r.OperationName,
		ObservedAt:   ts,
	}, true
}

func (s *Source) listFiles() ([]string, error) {
	fi, err := os.Stat(s.path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return []string{s.path}, nil
	}
	entries, err := os.ReadDir(s.path)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if n := e.Name(); strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".json.gz") {
			files = append(files, filepath.Join(s.path, n))
		}
	}
	sort.Strings(files)
	return files, nil
}

func readRecords(path string) ([]record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return recordsFromBytes(data), nil
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

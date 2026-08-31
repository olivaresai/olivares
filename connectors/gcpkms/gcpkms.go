// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gcpkms

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
const Name = "olivares.gcp-kms"

// signalGCPAudit is the SignalSource stamped on every edge. SignalSource is an open
// string (S02 §6), so a connector may introduce its own without an SDK release.
const signalGCPAudit = model.SignalSource("gcp_audit")

// Source is the Google Cloud KMS / Secret Manager audit connector. It satisfies
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

// New returns a gcp-kms source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Google Cloud KMS & Secret Manager (Cloud Audit Logs)",
		Description: "Observes Cloud KMS and Secret Manager use from Cloud Audit Logs (who used which key/secret), read-only. Never reads a secret value or key material.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "Cloud Audit Logs export file, or a directory of *.json / *.json.gz (NDJSON or entries[] wrapper)."},
		},
	}
}

// Open reads and validates configuration.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("gcp-kms: path is required")
	}
	return nil
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// Gather reads the configured export and emits one edge per recognized Cloud KMS /
// Secret Manager entry that names a key or secret.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	files, err := s.listFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := readEntries(f)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			edge, ok := s.buildEdge(e)
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

// Snapshot exposes the GCP secret-manager custodians seen in the export as
// secret_store NHIs. With no configured path it returns an empty graph.
func (s *Source) Snapshot(_ context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceGCPKMS, CapturedAt: s.clock().UTC()}
	if s.path == "" {
		return g, nil
	}
	files, err := s.listFiles()
	if err != nil {
		return identitysource.Graph{}, err
	}
	stores := map[string]store{}
	for _, f := range files {
		entries, err := readEntries(f)
		if err != nil {
			return identitysource.Graph{}, err
		}
		for _, e := range entries {
			if st, ok := storeFor(e); ok {
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
			Source:      identitysource.SourceGCPKMS,
			Attributes:  map[string]string{"provider": "gcp", "service": st.service, "project": st.project},
		})
	}
	return g, nil
}

// buildEdge maps one Cloud Audit entry to an edge, or ok=false for a non-KMS/
// non-Secret entry, one that names no resource or principal, or an unparseable
// timestamp.
func (s *Source) buildEdge(e entry) (model.EdgeObservation, bool) {
	kind, mode, ok := classify(e)
	if !ok {
		return model.EdgeObservation{}, false
	}
	ref := strings.TrimSpace(e.ProtoPayload.ResourceName)
	principal := strings.TrimSpace(e.ProtoPayload.AuthenticationInfo.PrincipalEmail)
	if ref == "" || principal == "" {
		return model.EdgeObservation{}, false
	}
	ts, ok := parseTime(e.Timestamp)
	if !ok {
		return model.EdgeObservation{}, false
	}
	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    principal,
		ResourceKind: kind,
		ResourceRef:  ref,
		Mode:         mode,
		Source:       signalGCPAudit,
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      e.ProtoPayload.MethodName,
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

func readEntries(path string) ([]entry, error) {
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
	return entriesFromBytes(data), nil
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

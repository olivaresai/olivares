// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sapodata

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/internal/content"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.sapodata-content"

// Source is the SAP OData content source. The zero value is not usable; call New.
type Source struct {
	content.Store
	path string
	mode string
	live *liveClient
}

var _ contentsource.Source = (*Source)(nil)

// New returns an SAP OData content source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeContentSource,
		Title:       "SAP OData v4 (content)",
		Description: "Ingests SAP entity content via OData v4 for knowledge bases (read-only, with PFCG authorization-group ACL).",
		Surfaces:    []string{"knowledge.document"},
		ConfigFields: []sdk.ConfigField{
			{Key: "export_path", Type: sdk.FieldString, Description: "path to an OData v4 JSON export file or a directory of *.json files"},
			{Key: "base_url", Type: sdk.FieldString, Description: "SAP system base URL (e.g. https://sap.example.com)"},
			{Key: "service_path", Type: sdk.FieldString, Description: "OData service path (e.g. /sap/opu/odata4/sap/API_MATERIAL/)"},
			{Key: "credential_ref", Type: sdk.FieldString, Secret: true, Description: "secret-store reference for SAP credentials (e.g. vault:secret/data/sap#token); never an inline secret"},
			{Key: "auth_scheme", Type: sdk.FieldString, Description: "\"basic\" (default, on-prem SAP) or \"oauth2_btp\" (SAP BTP XSUAA client-credentials)"},
			{Key: "token_url", Type: sdk.FieldString, Description: "XSUAA token endpoint for oauth2_btp auth scheme"},
			{Key: "entity_sets", Type: sdk.FieldString, Description: "comma-separated entity set names to sync (e.g. Materials,PurchaseOrders)"},
			{Key: "mode", Type: sdk.FieldString, Description: "\"export\" (default) or \"live\" (OData v4 API)"},
		},
	}
}

// Kind declares this source ingests knowledge documents.
func (s *Source) Kind() contentsource.ContentClass { return contentsource.ClassDocument }

// Open validates configuration and either wires a live OData client or parses a
// static export, depending on the "mode" setting.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.mode = strings.TrimSpace(cfg.Get("mode"))
	if s.mode == "live" {
		lc, err := newLiveClient(cfg)
		if err != nil {
			return err
		}
		s.live = lc
		s.SetDocs(nil) // live mode does not pre-load a static index
		return nil
	}
	// export mode (default)
	if msg := content.ValidateCredentialRef(cfg.Get("credential_ref")); msg != "" {
		return errors.New("sapodata: " + msg)
	}
	s.path = strings.TrimSpace(cfg.Get("export_path"))
	if s.path == "" {
		s.SetDocs(nil)
		return nil
	}
	docs, err := s.parseExport()
	if err != nil {
		return err
	}
	s.SetDocs(docs)
	return nil
}

// List returns one page of document references (honoring ctx).
func (s *Source) List(ctx context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if s.live != nil {
		return s.listLive(ctx, cursor)
	}
	return s.Store.List(cursor)
}

// Fetch returns one document by id (honoring ctx). The body is raw.
func (s *Source) Fetch(ctx context.Context, docID string) (contentsource.Document, error) {
	if err := ctx.Err(); err != nil {
		return contentsource.Document{}, err
	}
	if s.live != nil {
		return s.fetchLive(ctx, docID)
	}
	return s.Store.Fetch(docID)
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// ---- native OData v4 JSON export shape ----------------------------------------

type odataResponse struct {
	Value []odataEntity `json:"value"`
}

type odataEntity struct {
	ID          string            `json:"@odata.id"`
	Etag        string            `json:"@odata.etag"`
	Name        string            `json:"Name"`
	Description string            `json:"Description"`
	ModifiedAt  string            `json:"LastChangeDateTime"`
	AuthGroup   string            `json:"AuthorizationGroup"`
	Attributes  map[string]string `json:"_attributes"`
}

// parseExport reads the configured OData export file(s) and maps each entity to
// a Document.
func (s *Source) parseExport() ([]contentsource.Document, error) {
	files, err := content.ExportFiles(s.path, ".json")
	if err != nil {
		return nil, err
	}
	var out []contentsource.Document
	for _, f := range files {
		var exp odataResponse
		if err := content.ReadJSON(f, &exp); err != nil {
			return nil, err
		}
		for _, e := range exp.Value {
			if strings.TrimSpace(e.ID) == "" {
				continue
			}
			out = append(out, toDocument(e))
		}
	}
	return out, nil
}

// toDocument maps one OData entity to a Document. The ACL is the entity's
// AuthorizationGroup (PFCG role reference); the SpaceRef encodes the entity set.
func toDocument(e odataEntity) contentsource.Document {
	var acl []string
	if g := strings.TrimSpace(e.AuthGroup); g != "" {
		acl = append(acl, "role:"+g)
	}
	// Derive entity set from the OData ID (e.g. "Materials('MAT001')" → "Materials").
	entitySet := extractEntitySet(e.ID)
	return contentsource.Document{
		Source:      contentsource.SourceSAPOData,
		DocID:       content.Truncate(e.ID, content.MaxRefLen),
		Title:       content.Truncate(e.Name, content.MaxTitleLen),
		Body:        content.Truncate(e.Description, content.MaxBodyBytes),
		ContentType: "text/plain",
		ACL:         content.CleanACL(acl),
		SpaceRef:    "entity_set:" + entitySet,
		ModifiedAt:  parseTime(e.ModifiedAt),
		Attributes:  e.Attributes,
	}
}

// extractEntitySet extracts the entity set name from an OData ID like
// "Materials('MAT001')" → "Materials". Falls back to the full ID.
func extractEntitySet(id string) string {
	if idx := strings.IndexByte(id, '('); idx > 0 {
		return id[:idx]
	}
	return id
}

// parseTime parses an RFC3339 timestamp, returning the zero time on failure.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

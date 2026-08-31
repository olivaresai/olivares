// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureaisearch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/internal/content"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.azureaisearch-content"

// Source is the Azure AI Search content source. The zero value is not usable;
// call New.
type Source struct {
	content.Store
	path string
	mode string
	live *liveClient
}

var _ contentsource.Source = (*Source)(nil)

// New returns an Azure AI Search content source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeContentSource,
		Title:       "Azure AI Search (content)",
		Description: "Ingests Azure AI Search indexed documents for knowledge bases (read-only, with security-filter ACL).",
		Surfaces:    []string{"knowledge.document"},
		ConfigFields: []sdk.ConfigField{
			{Key: "endpoint", Type: sdk.FieldString, Description: "Azure AI Search endpoint (e.g. https://mysearch.search.windows.net)"},
			{Key: "index_name", Type: sdk.FieldString, Description: "search index name"},
			{Key: "credential_ref", Type: sdk.FieldString, Secret: true, Description: "secret-store reference for API key or managed identity credential (e.g. azure-keyvault:search#key); never an inline secret"},
			{Key: "auth_scheme", Type: sdk.FieldString, Description: "\"api_key\" (default) or \"managed_identity\""},
			{Key: "security_field", Type: sdk.FieldString, Description: "field name containing ACL principals (empty = no ACL)"},
			{Key: "timestamp_field", Type: sdk.FieldString, Description: "field name for incremental sync filtering"},
			{Key: "content_field", Type: sdk.FieldString, Description: "field name containing document content (default: \"content\")"},
			{Key: "title_field", Type: sdk.FieldString, Description: "field name containing document title (default: \"title\")"},
			{Key: "mode", Type: sdk.FieldString, Description: "\"export\" (default) or \"live\""},
			{Key: "export_path", Type: sdk.FieldString, Description: "path to JSON export file(s)"},
		},
	}
}

// Kind declares this source ingests knowledge documents.
func (s *Source) Kind() contentsource.ContentClass { return contentsource.ClassDocument }

// Open validates configuration and either wires a live API client or parses a
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
		return errors.New("azureaisearch: " + msg)
	}
	s.path = strings.TrimSpace(cfg.Get("export_path"))
	if s.path == "" {
		s.SetDocs(nil)
		return nil
	}
	contentField := strings.TrimSpace(cfg.Get("content_field"))
	if contentField == "" {
		contentField = "content"
	}
	titleField := strings.TrimSpace(cfg.Get("title_field"))
	if titleField == "" {
		titleField = "title"
	}
	securityField := strings.TrimSpace(cfg.Get("security_field"))
	timestampField := strings.TrimSpace(cfg.Get("timestamp_field"))
	indexName := strings.TrimSpace(cfg.Get("index_name"))

	docs, err := s.parseExport(contentField, titleField, securityField, timestampField, indexName)
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

// ---- Azure AI Search REST response shape --------------------------------------

// searchResponse is the top-level shape of an Azure AI Search REST response.
type searchResponse struct {
	Value    []map[string]any `json:"value"`
	NextLink string           `json:"@odata.nextLink"`
}

// parseExport reads the configured Azure AI Search export file(s) and maps each
// document to a contentsource.Document.
func (s *Source) parseExport(contentField, titleField, securityField, timestampField, indexName string) ([]contentsource.Document, error) {
	files, err := content.ExportFiles(s.path, ".json")
	if err != nil {
		return nil, err
	}
	var out []contentsource.Document
	for _, f := range files {
		var exp searchResponse
		if err := content.ReadJSON(f, &exp); err != nil {
			return nil, err
		}
		for _, raw := range exp.Value {
			doc, ok := mapDocument(raw, contentField, titleField, securityField, timestampField, indexName)
			if !ok {
				continue
			}
			out = append(out, doc)
		}
	}
	return out, nil
}

// mapDocument converts a raw Azure AI Search document map to a Document.
// It returns false if the document has no usable ID.
func mapDocument(raw map[string]any, contentField, titleField, securityField, timestampField, indexName string) (contentsource.Document, bool) {
	docID := extractDocID(raw)
	if docID == "" {
		return contentsource.Document{}, false
	}

	title := stringField(raw, titleField)
	body := stringField(raw, contentField)
	acl := extractACL(raw, securityField)
	modifiedAt := parseTimestampField(raw, timestampField)

	attrs := map[string]string{}
	if score, ok := raw["@search.score"]; ok {
		attrs["search_score"] = fmt.Sprintf("%v", score)
	}

	return contentsource.Document{
		Source:      contentsource.SourceAzureAISearch,
		DocID:       content.Truncate(docID, content.MaxRefLen),
		Title:       content.Truncate(title, content.MaxTitleLen),
		Body:        content.Truncate(body, content.MaxBodyBytes),
		ContentType: "text/plain",
		ACL:         content.CleanACL(acl),
		SpaceRef:    "index:" + indexName,
		ModifiedAt:  modifiedAt,
		Attributes:  attrs,
	}, true
}

// extractDocID resolves the document key: first tries "id", then "@search.key",
// then the first string value found.
func extractDocID(raw map[string]any) string {
	if v, ok := raw["id"]; ok {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	if v, ok := raw["@search.key"]; ok {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	// Fallback: first string field.
	for _, v := range raw {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// stringField extracts a string value from a map by field name.
func stringField(raw map[string]any, field string) string {
	if field == "" {
		return ""
	}
	v, ok := raw[field]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// extractACL builds ACL entries from the configured security field. If the field
// is a string array, each element becomes "principal:<value>". If it is a single
// string, it is split by comma.
func extractACL(raw map[string]any, securityField string) []string {
	if securityField == "" {
		return nil
	}
	v, ok := raw[securityField]
	if !ok {
		return nil
	}
	switch val := v.(type) {
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, "principal:"+strings.TrimSpace(s))
			}
		}
		return out
	case string:
		parts := strings.Split(val, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, "principal:"+p)
			}
		}
		return out
	default:
		return nil
	}
}

// parseTimestampField parses a timestamp from the named field, returning the zero
// time on failure.
func parseTimestampField(raw map[string]any, field string) time.Time {
	s := stringField(raw, field)
	return parseTime(s)
}

// parseTime parses an RFC3339 timestamp, returning the zero time on failure.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

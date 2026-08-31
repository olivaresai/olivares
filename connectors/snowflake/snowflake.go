// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package snowflake

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
const Name = "olivares.snowflake-content"

// Source is the Snowflake content source. The zero value is not usable; call New.
type Source struct {
	content.Store
	path string
	mode string
	live *liveClient
}

var _ contentsource.Source = (*Source)(nil)

// New returns a Snowflake content source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeContentSource,
		Title:       "Snowflake (content)",
		Description: "Ingests Snowflake data warehouse content for knowledge bases (read-only, with RBAC grant-based ACL as provenance permissions).",
		Surfaces:    []string{"knowledge.document"},
		ConfigFields: []sdk.ConfigField{
			{Key: "account", Type: sdk.FieldString, Description: "Snowflake account identifier (e.g. xy12345.us-east-1)"},
			{Key: "user", Type: sdk.FieldString, Description: "Snowflake user"},
			{Key: "credential_ref", Type: sdk.FieldString, Secret: true, Description: "secret-store reference for the RSA private key (e.g. vault:secret/data/snowflake#key); never an inline secret"},
			{Key: "warehouse", Type: sdk.FieldString, Description: "Snowflake warehouse"},
			{Key: "database", Type: sdk.FieldString, Description: "Snowflake database"},
			{Key: "schema_name", Type: sdk.FieldString, Description: "Snowflake schema"},
			{Key: "tables", Type: sdk.FieldString, Description: "comma-separated table/view names to sync"},
			{Key: "timestamp_column", Type: sdk.FieldString, Description: "column for incremental sync (default: LAST_MODIFIED)"},
			{Key: "mode", Type: sdk.FieldString, Description: "\"export\" (default) or \"live\" (Snowflake SQL REST API)"},
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
		return errors.New("snowflake: " + msg)
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

// ---- Snowflake JSON export shape ------------------------------------------------

type sfExport struct {
	Rows []sfRow `json:"rows"`
}

type sfRow struct {
	ID           string            `json:"id"`
	TableName    string            `json:"table_name"`
	Content      string            `json:"content"`
	Title        string            `json:"title"`
	ModifiedAt   string            `json:"modified_at"`
	GrantedRoles []string          `json:"granted_roles"`
	ShareName    string            `json:"share_name"`
	Attributes   map[string]string `json:"attributes"`
}

// parseExport reads the configured Snowflake export file(s) and maps each row
// to a Document.
func (s *Source) parseExport() ([]contentsource.Document, error) {
	files, err := content.ExportFiles(s.path, ".json")
	if err != nil {
		return nil, err
	}
	var out []contentsource.Document
	for _, f := range files {
		var exp sfExport
		if err := content.ReadJSON(f, &exp); err != nil {
			return nil, err
		}
		for _, r := range exp.Rows {
			if strings.TrimSpace(r.ID) == "" {
				continue
			}
			out = append(out, toDocument(r))
		}
	}
	return out, nil
}

// toDocument maps one Snowflake row to a Document. The ACL is the granted
// roles as "role:<name>" entries. The share_name is preserved as an attribute.
func toDocument(r sfRow) contentsource.Document {
	acl := make([]string, 0, len(r.GrantedRoles))
	for _, role := range r.GrantedRoles {
		role = strings.TrimSpace(role)
		if role != "" {
			acl = append(acl, "role:"+role)
		}
	}
	attrs := make(map[string]string, len(r.Attributes)+1)
	for k, v := range r.Attributes {
		attrs[k] = v
	}
	if sn := strings.TrimSpace(r.ShareName); sn != "" {
		attrs["share_name"] = sn
	}
	return contentsource.Document{
		Source:      contentsource.SourceSnowflake,
		DocID:       content.Truncate(r.ID, content.MaxRefLen),
		Title:       content.Truncate(r.Title, content.MaxTitleLen),
		Body:        content.Truncate(r.Content, content.MaxBodyBytes),
		ContentType: "text/plain",
		ACL:         content.CleanACL(acl),
		SpaceRef:    "table:" + r.TableName,
		ModifiedAt:  parseTime(r.ModifiedAt),
		Attributes:  attrs,
	}
}

// parseTime parses an RFC3339 timestamp, returning the zero time on failure.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package notion

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
const Name = "olivares.notion-content"

// Source is the Notion content source. The zero value is not usable; call New.
type Source struct {
	content.Store
	path string
	mode string
	live *liveClient
}

var _ contentsource.Source = (*Source)(nil)

// New returns a Notion content source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeContentSource,
		Title:       "Notion (content)",
		Description: "Ingests Notion page content for knowledge bases (read-only, with parent database and shared groups).",
		Surfaces:    []string{"knowledge.document"},
		ConfigFields: []sdk.ConfigField{
			{Key: "export_path", Type: sdk.FieldString, Description: "path to a Notion pages/blocks JSON file or a directory of *.json files"},
			{Key: "credential_ref", Type: sdk.FieldString, Secret: true, Description: "secret-store reference for the Notion integration token (e.g. vault:secret/data/notion#token); never an inline secret"},
			{Key: "mode", Type: sdk.FieldString, Description: "\"export\" (default) or \"live\" (Notion API v1)"},
			{Key: "base_url", Type: sdk.FieldString, Description: "Notion API base URL for live mode (default: https://api.notion.com/v1)"},
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
		return errors.New("notion: " + msg)
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

// ---- native Notion export shape -------------------------------------------------

type notionExport struct {
	Pages []notionPage `json:"pages"`
}

type notionPage struct {
	ID         string `json:"id"`
	Properties struct {
		Title struct {
			Title []struct {
				PlainText string `json:"plain_text"`
			} `json:"title"`
		} `json:"title"`
	} `json:"properties"`
	Parent struct {
		Type       string `json:"type"`
		DatabaseID string `json:"database_id"`
	} `json:"parent"`
	LastEditedTime string             `json:"last_edited_time"`
	Permissions    []notionPermission `json:"permissions"`
	Classification string             `json:"classification"`
	Blocks         []notionBlock      `json:"blocks"`
}

type notionPermission struct {
	Type      string `json:"type"`       // "group" | "user"
	GroupName string `json:"group_name"` // for group
	UserID    string `json:"user_id"`    // for user (reference)
}

type notionBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// parseExport reads the configured Notion export file(s) and maps each page to a
// Document.
func (s *Source) parseExport() ([]contentsource.Document, error) {
	files, err := content.ExportFiles(s.path, ".json")
	if err != nil {
		return nil, err
	}
	var out []contentsource.Document
	for _, f := range files {
		var exp notionExport
		if err := content.ReadJSON(f, &exp); err != nil {
			return nil, err
		}
		for _, p := range exp.Pages {
			if strings.TrimSpace(p.ID) == "" {
				continue
			}
			out = append(out, toDocument(p))
		}
	}
	return out, nil
}

// toDocument maps one Notion page to a Document. The body is the page's blocks
// joined as markdown; the ACL is the page's shared groups (references).
func toDocument(p notionPage) contentsource.Document {
	var title string
	if len(p.Properties.Title.Title) > 0 {
		title = p.Properties.Title.Title[0].PlainText
	}
	var sb strings.Builder
	for _, b := range p.Blocks {
		if t := strings.TrimSpace(b.Text); t != "" {
			sb.WriteString(t)
			sb.WriteString("\n\n")
		}
	}
	acl := make([]string, 0, len(p.Permissions))
	for _, perm := range p.Permissions {
		switch strings.ToLower(perm.Type) {
		case "group":
			if perm.GroupName != "" {
				acl = append(acl, "group:"+perm.GroupName)
			}
		case "user":
			if perm.UserID != "" {
				acl = append(acl, "user:"+perm.UserID)
			}
		}
	}
	space := ""
	if p.Parent.DatabaseID != "" {
		space = "database:" + p.Parent.DatabaseID
	}
	return contentsource.Document{
		Source:         contentsource.SourceNotion,
		DocID:          content.Truncate(p.ID, content.MaxRefLen),
		Title:          content.Truncate(title, content.MaxTitleLen),
		Body:           content.Truncate(strings.TrimSpace(sb.String()), content.MaxBodyBytes),
		ContentType:    "text/markdown",
		ACL:            content.CleanACL(acl),
		Classification: strings.TrimSpace(p.Classification),
		SpaceRef:       space,
		ModifiedAt:     parseTime(p.LastEditedTime),
		Attributes:     map[string]string{"parent_type": p.Parent.Type},
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

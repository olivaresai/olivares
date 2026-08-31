// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package confluence

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
const Name = "olivares.confluence-content"

// Source is the Confluence content source. The zero value is not usable; call New.
type Source struct {
	content.Store
	path string
	mode string
	live *liveClient
}

var _ contentsource.Source = (*Source)(nil)

// New returns a Confluence content source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeContentSource,
		Title:       "Atlassian Confluence (content)",
		Description: "Ingests Confluence page content for knowledge bases (read-only, with space, read-restrictions and labels).",
		Surfaces:    []string{"knowledge.document"},
		ConfigFields: []sdk.ConfigField{
			{Key: "export_path", Type: sdk.FieldString, Description: "path to a Confluence content REST JSON file or a directory of *.json files"},
			{Key: "credential_ref", Type: sdk.FieldString, Secret: true, Description: "secret-store reference for the Confluence API token (e.g. vault:secret/data/confluence#token); never an inline secret"},
			{Key: "mode", Type: sdk.FieldString, Description: "\"export\" (default) or \"live\" (Confluence Cloud REST API v2)"},
			{Key: "space_key", Type: sdk.FieldString, Description: "Confluence space key for live mode (e.g. ENG)"},
			{Key: "base_url", Type: sdk.FieldString, Description: "Confluence instance base URL for live mode (e.g. https://example.atlassian.net)"},
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
		return errors.New("confluence: " + msg)
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

// ---- native Confluence Content REST export shape --------------------------------

type confluenceExport struct {
	Results []confluencePage `json:"results"`
}

type confluencePage struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
	Space struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"space"`
	Body struct {
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
	Version struct {
		When string `json:"when"`
	} `json:"version"`
	Restrictions struct {
		Read struct {
			Restrictions struct {
				Group struct {
					Results []confluenceName `json:"results"`
				} `json:"group"`
			} `json:"restrictions"`
		} `json:"read"`
	} `json:"restrictions"`
	Metadata struct {
		Labels struct {
			Results []confluenceName `json:"results"`
		} `json:"labels"`
	} `json:"metadata"`
}

type confluenceName struct {
	Name string `json:"name"`
}

// parseExport reads the configured Confluence export file(s) and maps each page to
// a Document.
func (s *Source) parseExport() ([]contentsource.Document, error) {
	files, err := content.ExportFiles(s.path, ".json")
	if err != nil {
		return nil, err
	}
	var out []contentsource.Document
	for _, f := range files {
		var exp confluenceExport
		if err := content.ReadJSON(f, &exp); err != nil {
			return nil, err
		}
		for _, p := range exp.Results {
			if strings.TrimSpace(p.ID) == "" {
				continue
			}
			out = append(out, toDocument(p))
		}
	}
	return out, nil
}

// toDocument maps one Confluence page to a Document. The ACL is the read
// restrictions' groups (references); the classification is the first label.
func toDocument(p confluencePage) contentsource.Document {
	acl := make([]string, 0, len(p.Restrictions.Read.Restrictions.Group.Results))
	for _, g := range p.Restrictions.Read.Restrictions.Group.Results {
		if g.Name != "" {
			acl = append(acl, "group:"+g.Name)
		}
	}
	classification := ""
	if len(p.Metadata.Labels.Results) > 0 {
		classification = strings.TrimSpace(p.Metadata.Labels.Results[0].Name)
	}
	return contentsource.Document{
		Source:         contentsource.SourceConfluence,
		DocID:          content.Truncate(p.ID, content.MaxRefLen),
		Title:          content.Truncate(p.Title, content.MaxTitleLen),
		Body:           content.Truncate(p.Body.Storage.Value, content.MaxBodyBytes),
		ContentType:    "text/html",
		ACL:            content.CleanACL(acl),
		Classification: classification,
		SpaceRef:       "space:" + p.Space.Key,
		ModifiedAt:     parseTime(p.Version.When),
		Attributes:     map[string]string{"space_name": p.Space.Name, "type": p.Type},
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

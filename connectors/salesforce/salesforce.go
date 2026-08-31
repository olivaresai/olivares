// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package salesforce

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
const Name = "olivares.salesforce-content"

// Source is the Salesforce content source. The zero value is not usable; call New.
type Source struct {
	content.Store
	path string
	mode string
	live *liveClient
}

var _ contentsource.Source = (*Source)(nil)

// New returns a Salesforce content source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeContentSource,
		Title:       "Salesforce (content)",
		Description: "Ingests Salesforce CRM object content for knowledge bases (read-only, with sharing-model ACL as provenance permissions).",
		Surfaces:    []string{"knowledge.document"},
		ConfigFields: []sdk.ConfigField{
			{Key: "base_url", Type: sdk.FieldString, Description: "Salesforce instance URL (e.g. https://myorg.my.salesforce.com)"},
			{Key: "credential_ref", Type: sdk.FieldString, Secret: true, Description: "secret-store reference for the JWT private key (e.g. vault:secret/data/salesforce#key); never an inline secret"},
			{Key: "client_id", Type: sdk.FieldString, Description: "OAuth connected app consumer key"},
			{Key: "username", Type: sdk.FieldString, Description: "Salesforce user for JWT subject"},
			{Key: "login_url", Type: sdk.FieldString, Description: "OAuth token endpoint (default https://login.salesforce.com)"},
			{Key: "sobject_types", Type: sdk.FieldString, Description: "comma-separated SObject types to sync (default: Account,Case,Knowledge__kav,ContentDocument)"},
			{Key: "mode", Type: sdk.FieldString, Description: "\"export\" (default) or \"live\" (Salesforce REST API)"},
			{Key: "export_path", Type: sdk.FieldString, Description: "path to Salesforce REST JSON export file(s)"},
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
		return errors.New("salesforce: " + msg)
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

// ---- native Salesforce REST API query result shape ----------------------------

type sfQueryResult struct {
	Records        []sfRecord `json:"records"`
	NextRecordsURL string     `json:"nextRecordsUrl"`
	Done           bool       `json:"done"`
}

type sfRecord struct {
	Attributes struct {
		Type string `json:"type"`
	} `json:"attributes"`
	ID             string `json:"Id"`
	Name           string `json:"Name"`
	Description    string `json:"Description"`
	SystemModstamp string `json:"SystemModstamp"`
	SharingModel   string `json:"SharingModel"`
	OwnerID        string `json:"OwnerId"`
}

// parseExport reads the configured Salesforce export file(s) and maps each
// record to a Document.
func (s *Source) parseExport() ([]contentsource.Document, error) {
	files, err := content.ExportFiles(s.path, ".json")
	if err != nil {
		return nil, err
	}
	var out []contentsource.Document
	for _, f := range files {
		var qr sfQueryResult
		if err := content.ReadJSON(f, &qr); err != nil {
			return nil, err
		}
		for _, r := range qr.Records {
			if strings.TrimSpace(r.ID) == "" {
				continue
			}
			out = append(out, toDocument(r))
		}
	}
	return out, nil
}

// toDocument maps one Salesforce record to a Document. The ACL carries the
// owner and sharing model as provenance references.
func toDocument(r sfRecord) contentsource.Document {
	var acl []string
	if ownerID := strings.TrimSpace(r.OwnerID); ownerID != "" {
		acl = append(acl, "owner:"+ownerID)
	}
	if sharing := strings.TrimSpace(r.SharingModel); sharing != "" {
		acl = append(acl, "sharing:"+sharing)
	}
	return contentsource.Document{
		Source:      contentsource.SourceSalesforce,
		DocID:       content.Truncate(r.ID, content.MaxRefLen),
		Title:       content.Truncate(r.Name, content.MaxTitleLen),
		Body:        content.Truncate(r.Description, content.MaxBodyBytes),
		ContentType: "text/plain",
		ACL:         content.CleanACL(acl),
		SpaceRef:    "sobject:" + r.Attributes.Type,
		ModifiedAt:  parseTime(r.SystemModstamp),
		Attributes:  map[string]string{"sobject_type": r.Attributes.Type},
	}
}

// parseTime parses an RFC3339 timestamp, returning the zero time on failure.
// Salesforce uses millisecond-precision timestamps (e.g. "2026-05-15T14:22:00.000Z")
// which are valid RFC3339.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

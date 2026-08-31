// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sharepoint

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
const Name = "olivares.sharepoint-content"

// Source is the SharePoint content source. The zero value is not usable; call New.
type Source struct {
	content.Store
	path string
	mode string
	live *liveClient
}

var _ contentsource.Source = (*Source)(nil)

// New returns a SharePoint content source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeContentSource,
		Title:       "Microsoft SharePoint (content)",
		Description: "Ingests SharePoint/OneDrive document content for knowledge bases (read-only, with site, granted groups and sensitivity label).",
		Surfaces:    []string{"knowledge.document"},
		ConfigFields: []sdk.ConfigField{
			{Key: "export_path", Type: sdk.FieldString, Description: "path to a Microsoft Graph driveItem JSON file or a directory of *.json files"},
			{Key: "credential_ref", Type: sdk.FieldString, Secret: true, Description: "secret-store reference for the Graph app credential (e.g. azure-keyvault:sharepoint#secret); never an inline secret"},
			{Key: "mode", Type: sdk.FieldString, Description: "\"export\" (default) or \"live\" (Microsoft Graph OAuth)"},
			{Key: "site_id", Type: sdk.FieldString, Description: "SharePoint site ID for live mode"},
			{Key: "drive_id", Type: sdk.FieldString, Description: "Drive ID for live mode (optional, defaults to site default drive)"},
		},
	}
}

// Kind declares this source ingests knowledge documents.
func (s *Source) Kind() contentsource.ContentClass { return contentsource.ClassDocument }

// Open validates configuration and either wires a live Graph client or parses
// a static export, depending on the "mode" setting.
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
		return errors.New("sharepoint: " + msg)
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

// ---- native Microsoft Graph driveItem export shape ------------------------------

type graphExport struct {
	Value []driveItem `json:"value"`
}

type driveItem struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ParentReference struct {
		SiteID string `json:"siteId"`
		Path   string `json:"path"`
	} `json:"parentReference"`
	LastModifiedDateTime string            `json:"lastModifiedDateTime"`
	Permissions          []graphPermission `json:"permissions"`
	Content              string            `json:"content"`
	SensitivityLabel     struct {
		DisplayName string `json:"displayName"`
	} `json:"sensitivityLabel"`
	// Deleted is non-nil when the item has been removed (delta responses only).
	Deleted *struct {
		State string `json:"state"`
	} `json:"deleted"`
}

type graphPermission struct {
	GrantedToV2 struct {
		SiteGroup struct {
			DisplayName string `json:"displayName"`
		} `json:"siteGroup"`
		Group struct {
			DisplayName string `json:"displayName"`
		} `json:"group"`
	} `json:"grantedToV2"`
}

// parseExport reads the configured SharePoint export file(s) and maps each
// driveItem to a Document.
func (s *Source) parseExport() ([]contentsource.Document, error) {
	files, err := content.ExportFiles(s.path, ".json")
	if err != nil {
		return nil, err
	}
	var out []contentsource.Document
	for _, f := range files {
		var exp graphExport
		if err := content.ReadJSON(f, &exp); err != nil {
			return nil, err
		}
		for _, item := range exp.Value {
			if strings.TrimSpace(item.ID) == "" {
				continue
			}
			out = append(out, toDocument(item))
		}
	}
	return out, nil
}

// toDocument maps one driveItem to a Document. The ACL is the granted site
// groups / groups (references); the classification is the sensitivity label.
// If a sensitivity label is present, it is also reflected in ExternalLabels
// as a "purview:<label>" entry for cross-axis enforcement.
func toDocument(item driveItem) contentsource.Document {
	acl := make([]string, 0, len(item.Permissions))
	for _, p := range item.Permissions {
		if g := p.GrantedToV2.SiteGroup.DisplayName; g != "" {
			acl = append(acl, "group:"+g)
		}
		if g := p.GrantedToV2.Group.DisplayName; g != "" {
			acl = append(acl, "group:"+g)
		}
	}
	var extLabels []string
	if label := strings.TrimSpace(item.SensitivityLabel.DisplayName); label != "" {
		extLabels = append(extLabels, "purview:"+strings.ToLower(label))
	}
	return contentsource.Document{
		Source:         contentsource.SourceSharePoint,
		DocID:          content.Truncate(item.ID, content.MaxRefLen),
		Title:          content.Truncate(item.Name, content.MaxTitleLen),
		Body:           content.Truncate(item.Content, content.MaxBodyBytes),
		ContentType:    "text/plain",
		ACL:            content.CleanACL(acl),
		Classification: strings.ToLower(strings.TrimSpace(item.SensitivityLabel.DisplayName)),
		ExternalLabels: extLabels,
		SpaceRef:       "site:" + item.ParentReference.SiteID,
		ModifiedAt:     parseTime(item.LastModifiedDateTime),
		Attributes:     map[string]string{"path": item.ParentReference.Path},
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

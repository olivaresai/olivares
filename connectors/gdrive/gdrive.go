// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gdrive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/internal/content"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.gdrive-content"

// Source is the Google Drive content source. The zero value is not usable; call
// New. It builds its document set once at Open and serves it via the embedded
// content.Store.
type Source struct {
	content.Store
	path string
	mode string
	live *liveClient
}

var _ contentsource.Source = (*Source)(nil)

// New returns a Google Drive content source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeContentSource,
		Title:       "Google Drive (content)",
		Description: "Ingests Google Drive document content for knowledge bases (read-only, with source permissions and provenance).",
		Surfaces:    []string{"knowledge.document"},
		ConfigFields: []sdk.ConfigField{
			{Key: "export_path", Type: sdk.FieldString, Description: "path to a Drive files.list/export JSON file or a directory of *.json files"},
			{Key: "credential_ref", Type: sdk.FieldString, Secret: true, Description: "secret-store reference for the Drive OAuth credential (e.g. vault:secret/data/gdrive#token); never an inline secret"},
			{Key: "mode", Type: sdk.FieldString, Description: "\"export\" (default) or \"live\" (Google Drive API v3 OAuth)"},
			{Key: "drive_id", Type: sdk.FieldString, Description: "shared Drive ID for live mode (optional, defaults to user's My Drive)"},
			{Key: "api_base", Type: sdk.FieldString, Description: "override base URL for the Drive API v3 (default: https://www.googleapis.com/drive/v3)"},
		},
	}
}

// Kind declares this source ingests knowledge documents (the boundary).
func (s *Source) Kind() contentsource.ContentClass { return contentsource.ClassDocument }

// Open validates configuration and either wires a live Drive API client or
// parses a static export, depending on the "mode" setting.
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
		return errors.New("gdrive: " + msg)
	}
	s.path = strings.TrimSpace(cfg.Get("export_path"))
	if s.path == "" {
		s.SetDocs(nil) // declared offline: an empty source, not a failure
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

// ---- native Drive Files API export shape ----------------------------------------

type driveExport struct {
	Files []driveFile `json:"files"`
}

type driveFile struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	MimeType        string            `json:"mimeType"`
	ModifiedTime    string            `json:"modifiedTime"`
	Parents         []string          `json:"parents"`
	Permissions     []drivePermission `json:"permissions"`
	ExportedContent string            `json:"exportedContent"`
	AppProperties   map[string]string `json:"appProperties"`
}

type drivePermission struct {
	ID           string `json:"id"`           // opaque, stable permission id (the non-PII principal ref)
	Type         string `json:"type"`         // "user" | "group" | "domain" | "anyone"
	EmailAddress string `json:"emailAddress"` // for user/group — PII (a personal/group address); NEVER stored in the ACL
	Domain       string `json:"domain"`       // for domain
	Role         string `json:"role"`         // reader/writer/owner (provenance only)
}

// parseExport reads the configured Drive export file(s) and maps each file to a
// contentsource.Document.
func (s *Source) parseExport() ([]contentsource.Document, error) {
	files, err := content.ExportFiles(s.path, ".json")
	if err != nil {
		return nil, err
	}
	var out []contentsource.Document
	for _, f := range files {
		var exp driveExport
		if err := content.ReadJSON(f, &exp); err != nil {
			return nil, err
		}
		for _, df := range exp.Files {
			if strings.TrimSpace(df.ID) == "" {
				continue
			}
			out = append(out, toDocument(df))
		}
	}
	return out, nil
}

// toDocument maps one Drive file to a Document, deriving the ACL from the file's
// permissions and the classification from a "sensitivity" app property if present.
//
// Minimal data (contentsource §, docs/SECURITY-HARDENING.md): the ACL holds the permission's
// OPAQUE id ("user:<id>" / "group:<id>"), NEVER the user's or group's email — a
// personal email is PII the knowledge store does not need (the deployment's guard
// maps the stable permission id to an identity). A user/group grant with no id is
// emitted as a hashed reference rather than dropped, so the document stays
// restricted (dropping it would make an empty ACL = unrestricted = a leak).
func toDocument(df driveFile) contentsource.Document {
	acl := make([]string, 0, len(df.Permissions))
	for _, p := range df.Permissions {
		switch strings.ToLower(p.Type) {
		case "group":
			acl = append(acl, "group:"+principalRef(p.ID, p.EmailAddress))
		case "user":
			acl = append(acl, "user:"+principalRef(p.ID, p.EmailAddress))
		case "domain":
			if p.Domain != "" {
				acl = append(acl, "domain:"+p.Domain)
			}
		case "anyone":
			acl = append(acl, "anyone")
		}
	}
	space := ""
	if len(df.Parents) > 0 {
		space = "folder:" + df.Parents[0]
	}
	// ExternalLabels: if the sensitivity_label app property is set, surface it
	// as a "gdrive:<value>" label for cross-axis enforcement (additive to Classification).
	var extLabels []string
	if label := strings.TrimSpace(df.AppProperties["sensitivity_label"]); label != "" {
		extLabels = append(extLabels, "gdrive:"+strings.ToLower(label))
	}
	return contentsource.Document{
		Source:         contentsource.SourceGDrive,
		DocID:          content.Truncate(df.ID, content.MaxRefLen),
		Title:          content.Truncate(df.Name, content.MaxTitleLen),
		Body:           content.Truncate(df.ExportedContent, content.MaxBodyBytes),
		ContentType:    contentTypeFor(df.MimeType),
		ACL:            content.CleanACL(acl),
		Classification: strings.TrimSpace(df.AppProperties["sensitivity"]),
		ExternalLabels: extLabels,
		SpaceRef:       space,
		ModifiedAt:     parseTime(df.ModifiedTime),
		Attributes:     map[string]string{"mime_type": df.MimeType},
	}
}

// principalRef returns a non-PII, stable principal reference for an ACL entry: the
// source's opaque permission id when present, else a short hash of the email (so a
// grant is never dropped — an empty ACL would be unrestricted — and the personal
// email never enters the knowledge store, docs/SECURITY-HARDENING.md).
func principalRef(id, email string) string {
	if strings.TrimSpace(id) != "" {
		return id
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return "h:" + hex.EncodeToString(sum[:])[:16]
}

// contentTypeFor maps a Drive mimeType to a coarse content type for the chunker.
func contentTypeFor(mime string) string {
	switch {
	case strings.Contains(mime, "document"):
		return "text/plain"
	case strings.Contains(mime, "spreadsheet"):
		return "text/csv"
	case strings.Contains(mime, "html"):
		return "text/html"
	case strings.Contains(mime, "markdown"):
		return "text/markdown"
	default:
		return "text/plain"
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

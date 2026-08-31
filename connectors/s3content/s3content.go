// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package s3content

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
const Name = "olivares.s3-content"

// Source is the object-storage content source. The zero value is not usable; call
// New.
type Source struct {
	content.Store
	path string
	mode string
	live *liveClient
}

var _ contentsource.Source = (*Source)(nil)

// New returns an object-storage content source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeContentSource,
		Title:       "Object storage (S3/R2/GCS content)",
		Description: "Ingests object-storage content for knowledge bases (read-only, with bucket/prefix, object ACL grants and tags). Distinct from the s3-cloudtrail R/RW access audit.",
		Surfaces:    []string{"knowledge.document"},
		ConfigFields: []sdk.ConfigField{
			{Key: "export_path", Type: sdk.FieldString, Description: "path to an object listing+content JSON file or a directory of *.json files"},
			{Key: "credential_ref", Type: sdk.FieldString, Secret: true, Description: "secret-store reference for the object-store credential (e.g. aws-secretsmanager:s3-reader); never an inline secret"},
			{Key: "mode", Type: sdk.FieldString, Description: "\"export\" (default) or \"live\" (S3-compatible API)"},
			{Key: "bucket", Type: sdk.FieldString, Description: "bucket name for live mode"},
			{Key: "prefix", Type: sdk.FieldString, Description: "object key prefix for live mode"},
			{Key: "region", Type: sdk.FieldString, Default: "us-east-1", Description: "AWS signing region (default us-east-1)"},
			{Key: "endpoint", Type: sdk.FieldString, Description: "optional S3-compatible endpoint override (R2/MinIO/GCS interop); when set, path-style addressing is used"},
			{Key: "path_style", Type: sdk.FieldBool, Description: "force path-style bucket addressing"},
			{Key: "access_key_id", Type: sdk.FieldString, Secret: true, Description: "AWS access key id (or AWS_ACCESS_KEY_ID env fallback)"},
			{Key: "secret_access_key", Type: sdk.FieldString, Secret: true, Description: "AWS secret access key (or AWS_SECRET_ACCESS_KEY env fallback)"},
			{Key: "session_token", Type: sdk.FieldString, Secret: true, Description: "optional AWS session token (or AWS_SESSION_TOKEN env fallback)"},
			{Key: "timeout", Type: sdk.FieldDuration, Default: "30s", Description: "Per-request HTTP timeout."},
		},
	}
}

// Kind declares this source ingests knowledge documents.
func (s *Source) Kind() contentsource.ContentClass { return contentsource.ClassDocument }

// Open validates configuration and either wires a live S3-compatible client or
// parses a static export, depending on the "mode" setting.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.mode = strings.TrimSpace(cfg.Get("mode"))
	if s.mode == "live" {
		lc, err := newLiveClient(cfg)
		if err != nil {
			return err
		}
		s.live = lc
		s.SetDocs(nil)
		return nil
	}
	if msg := content.ValidateCredentialRef(cfg.Get("credential_ref")); msg != "" {
		return errors.New("s3content: " + msg)
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

// ---- native object listing + content export shape ------------------------------

type s3Export struct {
	Bucket  string     `json:"bucket"`
	Objects []s3Object `json:"objects"`
}

type s3Object struct {
	Key          string `json:"key"`
	LastModified string `json:"lastModified"`
	ContentType  string `json:"contentType"`
	Body         string `json:"body"`
	ACL          struct {
		Grants []struct {
			Grantee struct {
				Type        string `json:"type"` // "Group" | "CanonicalUser"
				URI         string `json:"uri"`
				ID          string `json:"id"`          // canonical user id (the non-PII principal ref)
				DisplayName string `json:"displayName"` // optional, often PII — NEVER stored in the ACL
			} `json:"grantee"`
			Permission string `json:"permission"`
		} `json:"grants"`
	} `json:"acl"`
	Tagging map[string]string `json:"tagging"`
}

// parseExport reads the configured object export file(s) and maps each object to a
// Document.
func (s *Source) parseExport() ([]contentsource.Document, error) {
	files, err := content.ExportFiles(s.path, ".json")
	if err != nil {
		return nil, err
	}
	var out []contentsource.Document
	for _, f := range files {
		var exp s3Export
		if err := content.ReadJSON(f, &exp); err != nil {
			return nil, err
		}
		for _, obj := range exp.Objects {
			if strings.TrimSpace(obj.Key) == "" {
				continue
			}
			out = append(out, toDocument(exp.Bucket, obj))
		}
	}
	return out, nil
}

// toDocument maps one object to a Document. The DocID is "<bucket>/<key>"; the ACL
// is derived from the object's grants (group URIs / canonical users as references).
func toDocument(bucket string, obj s3Object) contentsource.Document {
	// Minimal data (contentsource §, docs/SECURITY-HARDENING.md): a CanonicalUser grant uses the
	// canonical user ID (a stable 64-char hex id, always present in an S3 ACL),
	// NEVER the optional DisplayName (often a person's name/email — PII).
	acl := make([]string, 0, len(obj.ACL.Grants))
	for _, g := range obj.ACL.Grants {
		switch strings.ToLower(g.Grantee.Type) {
		case "group":
			if g.Grantee.URI != "" {
				acl = append(acl, "s3group:"+lastSegment(g.Grantee.URI))
			}
		case "canonicaluser":
			if g.Grantee.ID != "" {
				acl = append(acl, "user:"+g.Grantee.ID)
			}
		}
	}
	ctype := strings.TrimSpace(obj.ContentType)
	if ctype == "" {
		ctype = "text/plain"
	}
	docID := bucket + "/" + obj.Key
	return contentsource.Document{
		Source:         contentsource.SourceS3,
		DocID:          content.Truncate(docID, content.MaxRefLen),
		Title:          content.Truncate(lastSegment(obj.Key), content.MaxTitleLen),
		Body:           content.Truncate(obj.Body, content.MaxBodyBytes),
		ContentType:    ctype,
		ACL:            content.CleanACL(acl),
		Classification: strings.TrimSpace(obj.Tagging["classification"]),
		SpaceRef:       "s3:" + bucket,
		ModifiedAt:     parseTime(obj.LastModified),
		Attributes:     map[string]string{"key": obj.Key},
	}
}

// lastSegment returns the trailing path segment of s (after the last '/'), or s.
func lastSegment(s string) string {
	if i := strings.LastIndexByte(s, '/'); i >= 0 && i+1 < len(s) {
		return s[i+1:]
	}
	return s
}

// parseTime parses an RFC3339 timestamp, returning the zero time on failure.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package s3content

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

func TestS3ContentMapsObjectWithACLAndTags(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"export_path": "testdata/s3.json"}}); err != nil {
		t.Fatal(err)
	}
	if s.Kind() != contentsource.ClassDocument {
		t.Fatalf("Kind = %q", s.Kind())
	}
	doc, err := s.Fetch(context.Background(), "acme-knowledge/docs/handbook.md")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Source != contentsource.SourceS3 || doc.Title != "handbook.md" {
		t.Errorf("unexpected doc: %+v", doc)
	}
	if doc.SpaceRef != "s3:acme-knowledge" {
		t.Errorf("SpaceRef = %q", doc.SpaceRef)
	}
	if doc.Classification != "internal" {
		t.Errorf("Classification = %q", doc.Classification)
	}
	// Canonical user grant uses the opaque id, never the DisplayName (PII).
	if got := strings.Join(doc.ACL, ","); got != "s3group:AuthenticatedUsers,user:a1b2c3d4e5f6" {
		t.Errorf("ACL = %q", got)
	}
	for _, a := range doc.ACL {
		if strings.Contains(a, "Bob") || strings.Contains(a, "@") {
			t.Errorf("ACL entry must not contain the DisplayName/email (PII): %q", a)
		}
	}
	if doc.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q", doc.ContentType)
	}
}

func TestS3ContentRejectsInlineCredential(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path":    "testdata/s3.json",
		"credential_ref": "AKIAIOSFODNN7EXAMPLEwJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY",
	}})
	if err == nil || !strings.Contains(err.Error(), "secret-store reference") {
		t.Fatalf("expected secret-ref rejection, got %v", err)
	}
}

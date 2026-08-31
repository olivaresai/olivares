// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package confluence

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

func TestConfluenceParsesPageWithRestrictionsAndLabel(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"export_path": "testdata/confluence.json"}}); err != nil {
		t.Fatal(err)
	}
	if s.Kind() != contentsource.ClassDocument {
		t.Fatalf("Kind = %q", s.Kind())
	}
	doc, err := s.Fetch(context.Background(), "12345")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Source != contentsource.SourceConfluence || doc.Title != "Service Restart Runbook" {
		t.Errorf("unexpected doc: %+v", doc)
	}
	if doc.SpaceRef != "space:ENG" {
		t.Errorf("SpaceRef = %q", doc.SpaceRef)
	}
	if doc.Classification != "internal" {
		t.Errorf("Classification = %q", doc.Classification)
	}
	if got := strings.Join(doc.ACL, ","); got != "group:engineering,group:sre" {
		t.Errorf("ACL = %q", got)
	}
	if doc.ContentType != "text/html" || !strings.Contains(doc.Body, "systemctl restart api") {
		t.Errorf("body/contentType wrong: %q / %q", doc.Body, doc.ContentType)
	}
}

func TestConfluenceRejectsInlineCredential(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path":    "testdata/confluence.json",
		"credential_ref": "ATATT3xFfGF0aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789abcdef",
	}})
	if err == nil || !strings.Contains(err.Error(), "secret-store reference") {
		t.Fatalf("expected secret-ref rejection, got %v", err)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package notion

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

func TestNotionJoinsBlocksAndMapsProvenance(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"export_path": "testdata/notion.json"}}); err != nil {
		t.Fatal(err)
	}
	doc, err := s.Fetch(context.Background(), "page-1")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Source != contentsource.SourceNotion || doc.Title != "Product Meeting Notes" {
		t.Errorf("unexpected doc: %+v", doc)
	}
	if doc.SpaceRef != "database:db-9" {
		t.Errorf("SpaceRef = %q", doc.SpaceRef)
	}
	if got := strings.Join(doc.ACL, ","); got != "group:product" {
		t.Errorf("ACL = %q", got)
	}
	if !strings.Contains(doc.Body, "Decisions") || !strings.Contains(doc.Body, "governed retrieval API") {
		t.Errorf("body should join blocks, got %q", doc.Body)
	}
	if doc.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q", doc.ContentType)
	}
}

func TestNotionRejectsInlineCredential(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path":    "testdata/notion.json",
		"credential_ref": "secret_abcdefghijklmnopqrstuvwxyz0123456789ABCDEF",
	}})
	if err == nil || !strings.Contains(err.Error(), "secret-store reference") {
		t.Fatalf("expected secret-ref rejection, got %v", err)
	}
}

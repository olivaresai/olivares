// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sapodata

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

func TestSAPODataParsesEntityWithACL(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"export_path": "testdata/sapodata.json"}}); err != nil {
		t.Fatal(err)
	}
	if s.Kind() != contentsource.ClassDocument {
		t.Fatalf("Kind = %q", s.Kind())
	}
	doc, err := s.Fetch(context.Background(), "Materials('MAT001')")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Source != contentsource.SourceSAPOData {
		t.Errorf("Source = %q, want sap_odata", doc.Source)
	}
	if doc.Title != "Steel Plate A4" {
		t.Errorf("Title = %q", doc.Title)
	}
	if doc.SpaceRef != "entity_set:Materials" {
		t.Errorf("SpaceRef = %q", doc.SpaceRef)
	}
	if doc.ContentType != "text/plain" {
		t.Errorf("ContentType = %q", doc.ContentType)
	}
	if got := strings.Join(doc.ACL, ","); got != "role:MM_PURCHASER" {
		t.Errorf("ACL = %q, want role:MM_PURCHASER", got)
	}
	if !strings.Contains(doc.Body, "DIN EN 10025") {
		t.Errorf("Body = %q, expected DIN EN 10025", doc.Body)
	}
	if doc.ModifiedAt.IsZero() {
		t.Error("ModifiedAt is zero")
	}
}

func TestSAPODataRejectsInlineCredential(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path":    "testdata/sapodata.json",
		"credential_ref": "ATATT3xFfGF0aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789abcdef",
	}})
	if err == nil || !strings.Contains(err.Error(), "secret-store reference") {
		t.Fatalf("expected secret-ref rejection, got %v", err)
	}
}

func TestSAPODataEmptyExport(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open with no export_path should succeed, got %v", err)
	}
	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("expected 0 refs, got %d", len(refs))
	}
	if next != "" {
		t.Errorf("expected empty cursor, got %q", next)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureaisearch

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

func TestAzureAISearchParsesDocumentWithSecurityFilter(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path":     "testdata/azureaisearch.json",
		"index_name":      "policies",
		"security_field":  "security_filter",
		"timestamp_field": "lastModified",
		"content_field":   "content",
		"title_field":     "title",
	}}); err != nil {
		t.Fatal(err)
	}
	if s.Kind() != contentsource.ClassDocument {
		t.Fatalf("Kind = %q", s.Kind())
	}
	doc, err := s.Fetch(context.Background(), "doc-policy-remote-work")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Source != contentsource.SourceAzureAISearch {
		t.Errorf("Source = %q, want azure_ai_search", doc.Source)
	}
	if doc.Title != "Remote Work Policy 2026" {
		t.Errorf("Title = %q", doc.Title)
	}
	if !strings.Contains(doc.Body, "hybrid remote work") {
		t.Errorf("Body = %q, want substring 'hybrid remote work'", doc.Body)
	}
	if doc.ContentType != "text/plain" {
		t.Errorf("ContentType = %q", doc.ContentType)
	}
	if doc.SpaceRef != "index:policies" {
		t.Errorf("SpaceRef = %q", doc.SpaceRef)
	}
	if doc.ModifiedAt.IsZero() {
		t.Error("ModifiedAt is zero")
	}
	// ACL should have two entries from the security_filter array.
	if got := strings.Join(doc.ACL, ","); got != "principal:hr_team,principal:all_managers" {
		t.Errorf("ACL = %q, want principal:hr_team,principal:all_managers", got)
	}
	// Attributes should contain the search score.
	if doc.Attributes["search_score"] != "1" {
		t.Errorf("Attributes[search_score] = %q, want 1", doc.Attributes["search_score"])
	}
}

func TestAzureAISearchRejectsInlineCredential(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path":    "testdata/azureaisearch.json",
		"credential_ref": "ATATT3xFfGF0aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789abcdef",
	}})
	if err == nil || !strings.Contains(err.Error(), "secret-store reference") {
		t.Fatalf("expected secret-ref rejection, got %v", err)
	}
}

func TestAzureAISearchEmptyExport(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Errorf("expected 0 refs, got %d", len(refs))
	}
	if next != "" {
		t.Errorf("expected empty cursor, got %q", next)
	}
}

func TestAzureAISearchNoSecurityField(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path":     "testdata/azureaisearch.json",
		"index_name":      "policies",
		"timestamp_field": "lastModified",
		// security_field intentionally absent
	}}); err != nil {
		t.Fatal(err)
	}
	doc, err := s.Fetch(context.Background(), "doc-policy-remote-work")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.ACL) != 0 {
		t.Errorf("ACL = %v, want empty (no security field configured)", doc.ACL)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sharepoint

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

func TestSharePointMapsGroupsAndSensitivity(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"export_path": "testdata/sharepoint.json"}}); err != nil {
		t.Fatal(err)
	}
	doc, err := s.Fetch(context.Background(), "item-1")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Source != contentsource.SourceSharePoint || doc.Title != "Leave Policy.docx" {
		t.Errorf("unexpected doc: %+v", doc)
	}
	if doc.SpaceRef != "site:site-HR" {
		t.Errorf("SpaceRef = %q", doc.SpaceRef)
	}
	if doc.Classification != "confidential" {
		t.Errorf("Classification = %q", doc.Classification)
	}
	if got := strings.Join(doc.ACL, ","); got != "group:HR Team,group:All Employees" {
		t.Errorf("ACL = %q", got)
	}
}

func TestSharePointRejectsInlineCredential(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path":    "testdata/sharepoint.json",
		"credential_ref": "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ012",
	}})
	if err == nil || !strings.Contains(err.Error(), "secret-store reference") {
		t.Fatalf("expected secret-ref rejection, got %v", err)
	}
}

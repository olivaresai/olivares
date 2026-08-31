// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package salesforce

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

func TestSalesforceParsesRecordWithACL(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"export_path": "testdata/salesforce.json"}}); err != nil {
		t.Fatal(err)
	}
	if s.Kind() != contentsource.ClassDocument {
		t.Fatalf("Kind = %q", s.Kind())
	}
	doc, err := s.Fetch(context.Background(), "ka0Dn000000EXAMPLE")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Source != contentsource.SourceSalesforce {
		t.Errorf("Source = %q, want salesforce", doc.Source)
	}
	if doc.Title != "VPN Setup Guide" {
		t.Errorf("Title = %q", doc.Title)
	}
	if doc.SpaceRef != "sobject:Knowledge__kav" {
		t.Errorf("SpaceRef = %q", doc.SpaceRef)
	}
	if doc.ContentType != "text/plain" {
		t.Errorf("ContentType = %q", doc.ContentType)
	}
	if !strings.Contains(doc.Body, "corporate VPN client") {
		t.Errorf("Body = %q", doc.Body)
	}
	if doc.ModifiedAt.IsZero() {
		t.Error("ModifiedAt is zero")
	}
	if doc.ModifiedAt.Year() != 2026 || doc.ModifiedAt.Month() != 5 || doc.ModifiedAt.Day() != 15 {
		t.Errorf("ModifiedAt = %v", doc.ModifiedAt)
	}
	if got := strings.Join(doc.ACL, ","); got != "owner:005Dn000001EXAMPLE,sharing:ReadWrite" {
		t.Errorf("ACL = %q", got)
	}
	if doc.Attributes["sobject_type"] != "Knowledge__kav" {
		t.Errorf("Attributes = %v", doc.Attributes)
	}
}

func TestSalesforceRejectsInlineCredential(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path":    "testdata/salesforce.json",
		"credential_ref": "MIIEvgIBADANBgkqhkiG9w0BAQEFAASCBKgwggSkAgEAAoIBAQC7aBcDeFgHiJkLm",
	}})
	if err == nil || !strings.Contains(err.Error(), "secret-store reference") {
		t.Fatalf("expected secret-ref rejection, got %v", err)
	}
}

func TestSalesforceEmptyExport(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Errorf("expected empty list, got %d", len(refs))
	}
	if next != "" {
		t.Errorf("expected empty cursor, got %q", next)
	}
}

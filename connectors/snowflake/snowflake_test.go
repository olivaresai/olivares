// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package snowflake

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

func TestSnowflakeParsesRowWithRolesAndShare(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"export_path": "testdata/snowflake.json"}}); err != nil {
		t.Fatal(err)
	}
	if s.Kind() != contentsource.ClassDocument {
		t.Fatalf("Kind = %q", s.Kind())
	}
	doc, err := s.Fetch(context.Background(), "PRODUCTS_ROW_42")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Source != contentsource.SourceSnowflake {
		t.Errorf("Source = %q, want snowflake", doc.Source)
	}
	if doc.Title != "Titanium Bolt M12" {
		t.Errorf("Title = %q", doc.Title)
	}
	if doc.SpaceRef != "table:ANALYTICS.PUBLIC.PRODUCTS" {
		t.Errorf("SpaceRef = %q", doc.SpaceRef)
	}
	if got := strings.Join(doc.ACL, ","); got != "role:ANALYST_ROLE,role:DATA_ENGINEER" {
		t.Errorf("ACL = %q", got)
	}
	if doc.ContentType != "text/plain" {
		t.Errorf("ContentType = %q", doc.ContentType)
	}
	if !strings.Contains(doc.Body, "titanium alloy bolt") {
		t.Errorf("Body = %q", doc.Body)
	}
	if doc.Attributes["share_name"] != "partner_catalog_share" {
		t.Errorf("Attributes[share_name] = %q", doc.Attributes["share_name"])
	}
	if doc.Attributes["warehouse"] != "COMPUTE_WH" {
		t.Errorf("Attributes[warehouse] = %q", doc.Attributes["warehouse"])
	}
	if doc.Attributes["database"] != "ANALYTICS" {
		t.Errorf("Attributes[database] = %q", doc.Attributes["database"])
	}
	if doc.ModifiedAt.IsZero() {
		t.Error("ModifiedAt is zero")
	}
}

func TestSnowflakeRejectsInlineCredential(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path":    "testdata/snowflake.json",
		"credential_ref": "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7Zz",
	}})
	if err == nil || !strings.Contains(err.Error(), "secret-store reference") {
		t.Fatalf("expected secret-ref rejection, got %v", err)
	}
}

func TestSnowflakeEmptyExport(t *testing.T) {
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

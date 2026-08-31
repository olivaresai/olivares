// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pgcontent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name {
		t.Fatalf("name = %q, want %q", d.Name, Name)
	}
	if d.Type != sdk.TypeContentSource {
		t.Fatalf("type = %v, want source", d.Type)
	}
	// The credential-bearing fields must be declared Secret so the engine resolves
	// them by reference and never persists them.
	wantSecret := map[string]bool{fDSN: true, fPasswordRef: true, fCredentialRef: true}
	got := map[string]bool{}
	for _, f := range d.ConfigFields {
		if f.Secret {
			got[f.Key] = true
		}
	}
	for k := range wantSecret {
		if !got[k] {
			t.Errorf("config field %q must be Secret", k)
		}
	}
}

func TestKindIsDocument(t *testing.T) {
	if k := New().Kind(); k != contentsource.ClassDocument {
		t.Fatalf("kind = %q, want document", k)
	}
}

func cfg(m map[string]string) sdk.Config { return sdk.Config{Settings: m} }

func TestConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]string
	}{
		{"inline credential rejected", map[string]string{
			fCredentialRef: "super-secret-inline-token", fKeyColumns: "id", fBodyColumns: "body",
		}},
		{"missing key_columns", map[string]string{fTable: "t", fBodyColumns: "body"}},
		{"missing body_columns", map[string]string{fTable: "t", fKeyColumns: "id"}},
		{"table and query both set", map[string]string{
			fTable: "t", fQuery: "SELECT 1", fKeyColumns: "id", fBodyColumns: "body",
		}},
		{"invalid table identifier", map[string]string{
			fTable: "t; DROP TABLE x", fKeyColumns: "id", fBodyColumns: "body",
		}},
		{"invalid column identifier", map[string]string{
			fTable: "t", fKeyColumns: "id", fBodyColumns: "body; DROP",
		}},
		{"non-SELECT query rejected", map[string]string{
			fQuery: "DELETE FROM t", fKeyColumns: "id", fBodyColumns: "body",
		}},
		{"live mode needs table or query", map[string]string{
			fMode: "live", fKeyColumns: "id", fBodyColumns: "body",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseConfig(cfg(tc.in)); err == nil {
				t.Errorf("expected a config error for %s", tc.name)
			}
		})
	}
}

func TestConfigValidCredentialRef(t *testing.T) {
	_, err := parseConfig(cfg(map[string]string{
		fMode: "export", fTable: "t", fKeyColumns: "id", fBodyColumns: "body",
		fCredentialRef: "vault:secret/data/pg#password",
	}))
	if err != nil {
		t.Fatalf("a valid secret-store reference must be accepted: %v", err)
	}
}

const exportJSON = `{
  "schema": "public",
  "table": "articles",
  "rows": [
    {"id": 1, "title": "Hello", "body": "world", "owner_group": "eng", "classification": "internal", "ssn": "123-45-6789", "url": "/a/1", "updated_at": "2026-07-01T00:00:00Z"},
    {"id": 2, "title": "Second", "body": "b2", "owner_group": "", "classification": "", "ssn": "", "url": "/a/2", "updated_at": "2026-07-02T00:00:00Z"}
  ]
}`

func exportSource(t *testing.T) *Source {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "articles.json")
	if err := os.WriteFile(path, []byte(exportJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New()
	err := s.Open(context.Background(), cfg(map[string]string{
		fMode:             "export",
		fExportPath:       path,
		fSchema:           "public",
		fTable:            "articles",
		fKeyColumns:       "id",
		fBodyColumns:      "body",
		fTitleColumn:      "title",
		fACLColumns:       "owner_group",
		fClassColumn:      "classification",
		fSensitiveColumns: "ssn",
		fMetadataColumns:  "url",
		fUpdatedAtColumn:  "updated_at",
	}))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func TestExportListAndFetchMapping(t *testing.T) {
	s := exportSource(t)
	defer func() { _ = s.Close(context.Background()) }()
	ctx := context.Background()

	refs, next, err := s.List(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("listed %d docs, want 2", len(refs))
	}
	if next != "" {
		t.Errorf("next cursor = %q, want empty (all listed)", next)
	}

	doc, err := s.Fetch(ctx, "postgres:public.articles#1")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if doc.Source != SourcePostgres {
		t.Errorf("source = %q, want postgres", doc.Source)
	}
	if doc.Title != "Hello" {
		t.Errorf("title = %q, want Hello", doc.Title)
	}
	if doc.Body != "world" {
		t.Errorf("body = %q, want world", doc.Body)
	}
	if len(doc.ACL) != 1 || doc.ACL[0] != "group:eng" {
		t.Errorf("acl = %v, want [group:eng]", doc.ACL)
	}
	if doc.Classification != "internal" {
		t.Errorf("classification = %q, want internal", doc.Classification)
	}
	if len(doc.ExternalLabels) != 1 || doc.ExternalLabels[0] != "pii:ssn" {
		t.Errorf("external labels = %v, want [pii:ssn]", doc.ExternalLabels)
	}
	if doc.Attributes["url"] != "/a/1" || doc.Attributes["table"] != "articles" {
		t.Errorf("attributes = %v, want url=/a/1 table=articles", doc.Attributes)
	}
	if doc.ModifiedAt.IsZero() {
		t.Error("modified_at should parse from updated_at")
	}

	// Row 2 has no owner_group and no ssn → inherits default ACL (empty) and carries
	// no external label (honest: only what the row expresses).
	doc2, err := s.Fetch(ctx, "postgres:public.articles#2")
	if err != nil {
		t.Fatalf("fetch row 2: %v", err)
	}
	if len(doc2.ACL) != 0 {
		t.Errorf("row 2 acl = %v, want empty (inherit default)", doc2.ACL)
	}
	if len(doc2.ExternalLabels) != 0 {
		t.Errorf("row 2 labels = %v, want none", doc2.ExternalLabels)
	}
}

func TestDocIDRoundTrip(t *testing.T) {
	sc := &sourceConfig{schema: "public", table: "t", keyColumns: []string{"a", "b"}}
	// Values with the separator characters must survive encode→decode.
	r := row{"a": "x/y:z#w", "b": "42"}
	id := sc.docID(r)
	keys, err := sc.decodeKeys(id)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(keys) != 2 || keys[0] != "x/y:z#w" || keys[1] != "42" {
		t.Fatalf("round-trip = %v, want [x/y:z#w 42]", keys)
	}
}

func TestLiveOnlyMethodsErrorInExportMode(t *testing.T) {
	s := exportSource(t)
	ctx := context.Background()
	if _, err := s.DeltaList(ctx, ""); err == nil {
		t.Error("DeltaList must error in export mode")
	}
	if _, err := s.FetchACL(ctx, "postgres:public.articles#1"); err == nil {
		t.Error("FetchACL must error in export mode")
	}
	if _, err := s.Discover(ctx, "public"); err == nil {
		t.Error("Discover must error in export mode")
	}
}

func TestOpenEmptyWhenNoSourceConfigured(t *testing.T) {
	// Export mode with no path, and live mode with no connection, both open as an
	// empty source (declared offline), never a hard failure.
	for _, m := range []string{"export", "live"} {
		s := New()
		err := s.Open(context.Background(), cfg(map[string]string{
			fMode: m, fTable: "t", fKeyColumns: "id", fBodyColumns: "body",
		}))
		if err != nil {
			t.Fatalf("%s mode with no source should open empty, got: %v", m, err)
		}
		refs, _, err := s.List(context.Background(), "")
		if err != nil {
			t.Fatalf("%s list: %v", m, err)
		}
		if len(refs) != 0 {
			t.Errorf("%s mode: expected empty source, got %d refs", m, len(refs))
		}
	}
}

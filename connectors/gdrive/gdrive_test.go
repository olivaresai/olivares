// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gdrive

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

func openWith(t *testing.T, settings map[string]string) *Source {
	t.Helper()
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestGDriveParsesDocumentsWithProvenanceAndACL(t *testing.T) {
	s := openWith(t, map[string]string{"export_path": "testdata/drive.json"})
	if s.Kind() != contentsource.ClassDocument {
		t.Fatalf("Kind = %q, want document", s.Kind())
	}
	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if next != "" {
		t.Fatalf("unexpected next cursor %q for a 2-doc export", next)
	}
	if len(refs) != 2 {
		t.Fatalf("List returned %d refs, want 2", len(refs))
	}

	doc, err := s.Fetch(context.Background(), "1aBcStrategy")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Source != contentsource.SourceGDrive {
		t.Errorf("Source = %q", doc.Source)
	}
	if doc.Title != "Q3 Strategy" {
		t.Errorf("Title = %q", doc.Title)
	}
	if doc.Classification != "confidential" {
		t.Errorf("Classification = %q, want confidential", doc.Classification)
	}
	if doc.SpaceRef != "folder:folderXYZ" {
		t.Errorf("SpaceRef = %q", doc.SpaceRef)
	}
	// The ACL uses opaque permission ids — NEVER the personal/group email (PII).
	if got := strings.Join(doc.ACL, ","); got != "group:perm-eng,user:perm-alice,domain:acme.com" {
		t.Errorf("ACL = %q", got)
	}
	for _, a := range doc.ACL {
		if strings.Contains(a, "@") {
			t.Errorf("ACL entry must not contain an email (PII): %q", a)
		}
	}
	// The body is returned RAW (the module redacts it): the secret-shaped token is
	// still present here, proving the connector does not pre-scrub the body.
	if !strings.Contains(doc.Body, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("connector must return raw body for the module to redact; got %q", doc.Body)
	}

	pub, err := s.Fetch(context.Background(), "2deHandbook")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(pub.ACL, ","); got != "anyone" {
		t.Errorf("public doc ACL = %q, want anyone", got)
	}
}

func TestGDriveRejectsInlineCredential(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path":    "testdata/drive.json",
		"credential_ref": "ya29.A0ARrdaInlineAccessTokenThatIsNotAReference1234567890",
	}})
	if err == nil {
		t.Fatal("expected Open to reject an inline credential, got nil")
	}
	if !strings.Contains(err.Error(), "secret-store reference") {
		t.Fatalf("error should explain the secret-ref rule, got %v", err)
	}
}

func TestGDriveAcceptsSecretRefAndEmptySource(t *testing.T) {
	// A valid secret-store reference is accepted; with no export it is an empty
	// source (declared offline), not a failure.
	s := openWith(t, map[string]string{"credential_ref": "vault:secret/data/gdrive#token"})
	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 || next != "" {
		t.Fatalf("empty source should list nothing, got %d refs / next %q", len(refs), next)
	}
	if _, err := s.Fetch(context.Background(), "anything"); err == nil {
		t.Fatal("Fetch on empty source should report not-found")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

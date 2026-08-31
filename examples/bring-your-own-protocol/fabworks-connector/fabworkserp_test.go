// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package fabworkserp

import (
	"context"
	"strings"
	"testing"

	fixture "example.com/fabworks/erp-fixture"

	"github.com/olivaresai/olivares/sdk"
)

// TestLifecycle drives the full Open -> List -> Fetch -> DeltaList -> FetchACL
// -> Close lifecycle exactly as the engine does, using the local FabWorks ERP
// fixture protocol server.
func TestLifecycle(t *testing.T) {
	ctx := context.Background()
	srv := fixture.NewServer()
	defer srv.Close()

	c := New()
	d := c.Descriptor()
	if d.Name != Name {
		t.Fatalf("Descriptor().Name = %q, want %q", d.Name, Name)
	}
	if d.Type != sdk.TypeContentSource {
		t.Fatalf("Descriptor().Type = %q, want %q", d.Type, sdk.TypeContentSource)
	}
	if len(d.Surfaces) != 1 || d.Surfaces[0] != "knowledge.document" {
		t.Fatalf("Descriptor().Surfaces = %v, want [knowledge.document]", d.Surfaces)
	}

	if err := c.Open(ctx, sdk.Config{Settings: map[string]string{
		"base_url": srv.URL,
		"token":    "resolved-test-secret",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	refs, next, err := c.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if next != "" {
		t.Fatalf("List next cursor = %q, want empty", next)
	}
	if len(refs) != 3 || refs[0].DocID != "po-1001" {
		t.Fatalf("List refs = %#v, want po-1001 first", refs)
	}
	doc, err := c.Fetch(ctx, refs[0].DocID)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(string(doc.Body), "gearbox assemblies") {
		t.Fatalf("Fetch body = %q, want FabWorks content", doc.Body)
	}
	if len(doc.ACL) != 2 || doc.ACL[0] != "group:engineering" {
		t.Fatalf("Fetch ACL = %v, want group refs", doc.ACL)
	}
	if doc.Attributes["uri"] != "fabworks://erp/purchase-orders/1001" {
		t.Fatalf("Fetch attributes = %#v, want ERP uri provenance", doc.Attributes)
	}

	page, err := c.DeltaList(ctx, "")
	if err != nil {
		t.Fatalf("DeltaList: %v", err)
	}
	if len(page.Changes) != 3 || page.ResumeToken != "after-initial" {
		t.Fatalf("DeltaList = %#v token=%q", page.Changes, page.ResumeToken)
	}
	if page.Changes[0].ChangeKind != sdk.ChangeContent || page.Changes[1].ChangeKind != sdk.ChangeACL || page.Changes[2].ChangeKind != sdk.ChangeDeleted {
		t.Fatalf("unexpected change kinds: %#v", page.Changes)
	}

	acl, err := c.FetchACL(ctx, "po-1001")
	if err != nil {
		t.Fatalf("FetchACL: %v", err)
	}
	if len(acl.ACL) != 2 || acl.Classification != "internal" {
		t.Fatalf("FetchACL = %#v", acl)
	}
	if err := c.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestListHonorsContext(t *testing.T) {
	c := newWithBackend(&fakeBackend{})
	if err := c.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"base_url": "https://content.example.invalid",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.List(ctx, ""); err == nil {
		t.Fatal("List with a canceled context should return the context error")
	}
}

type fakeBackend struct{}

func (f *fakeBackend) List(ctx context.Context, _ string) ([]sdk.DocRef, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return nil, "", nil
}

func (f *fakeBackend) Fetch(ctx context.Context, _ string) (sdk.Document, error) {
	if err := ctx.Err(); err != nil {
		return sdk.Document{}, err
	}
	return sdk.Document{}, nil
}

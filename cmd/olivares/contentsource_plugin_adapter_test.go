// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

// pagedSDKSource is an sdk.ContentSource that also implements bounded pagination, so the
// adapter must delegate to it and forward the per-call completeness (F5).
type pagedSDKSource struct {
	lastMaxItems, lastMaxBytes int
	complete                   bool
}

func (*pagedSDKSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "t.paged", Type: sdk.TypeContentSource, APIVersion: sdk.APIVersion}
}
func (*pagedSDKSource) Open(context.Context, sdk.Config) error { return nil }
func (*pagedSDKSource) Close(context.Context) error            { return nil }
func (*pagedSDKSource) List(context.Context, string) ([]sdk.DocRef, string, error) {
	return []sdk.DocRef{{DocID: "viaList"}}, "", nil
}
func (*pagedSDKSource) Fetch(_ context.Context, id string) (sdk.Document, error) {
	return sdk.Document{Source: "t", DocID: id}, nil
}
func (s *pagedSDKSource) ListPage(_ context.Context, _ string, maxItems, maxBytes int) ([]sdk.DocRef, string, bool, error) {
	s.lastMaxItems, s.lastMaxBytes = maxItems, maxBytes
	return []sdk.DocRef{{DocID: "viaListPage"}}, "next-cur", s.complete, nil
}

// plainSDKSource implements only the base sdk.ContentSource — the adapter's ListPage must
// fall back to List and report complete=true (its List is already bounded host-side).
type plainSDKSource struct{}

func (plainSDKSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "t.plain", Type: sdk.TypeContentSource, APIVersion: sdk.APIVersion}
}
func (plainSDKSource) Open(context.Context, sdk.Config) error { return nil }
func (plainSDKSource) Close(context.Context) error            { return nil }
func (plainSDKSource) List(context.Context, string) ([]sdk.DocRef, string, error) {
	return []sdk.DocRef{{DocID: "viaList"}}, "", nil
}
func (plainSDKSource) Fetch(_ context.Context, id string) (sdk.Document, error) {
	return sdk.Document{Source: "t", DocID: id}, nil
}

func TestSDKContentAdapterListPageDelegates(t *testing.T) {
	src := &pagedSDKSource{complete: false}
	wrapped := wrapSDKContentSource(src)

	paged, ok := wrapped.(contentsource.PagedSource)
	if !ok {
		t.Fatalf("adapter %T is not a contentsource.PagedSource", wrapped)
	}
	refs, next, complete, err := paged.ListPage(context.Background(), "", 123, 456)
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "viaListPage" {
		t.Fatalf("ListPage did not delegate to the sdk PagedContentSource: %+v", refs)
	}
	if next != "next-cur" {
		t.Errorf("resume cursor not forwarded: %q", next)
	}
	if complete {
		t.Errorf("per-call completeness must forward the sdk client value (want false)")
	}
	if src.lastMaxItems != 123 || src.lastMaxBytes != 456 {
		t.Errorf("ceilings not forwarded to the plugin: items=%d bytes=%d", src.lastMaxItems, src.lastMaxBytes)
	}
}

func TestSDKContentAdapterListPageFallsBackForPlainSource(t *testing.T) {
	wrapped := wrapSDKContentSource(plainSDKSource{})
	paged, ok := wrapped.(contentsource.PagedSource)
	if !ok {
		t.Fatalf("adapter %T is not a contentsource.PagedSource", wrapped)
	}
	refs, _, complete, err := paged.ListPage(context.Background(), "", 100, 0)
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "viaList" {
		t.Fatalf("ListPage fallback to List failed: %+v", refs)
	}
	if !complete {
		t.Errorf("a plain source's bounded List must report complete=true")
	}
}

// TestModeWrapperPreservesPagedSource is the F5 re-review finding-B regression: the
// production chain sdk client → wrapSDKContentSource (adapter) → wrapContentSourceMode must
// NOT erase the PagedSource capability, or the knowledge host's assertion fails and the whole
// bounded-wire + completeness fix is inert. It asserts a per-call complete=false survives BOTH
// wrappers.
func TestModeWrapperPreservesPagedSource(t *testing.T) {
	src := &pagedSDKSource{complete: false}
	wrapped := wrapContentSourceMode(wrapSDKContentSource(src), "export")

	paged, ok := wrapped.(contentsource.PagedSource)
	if !ok {
		t.Fatalf("mode-wrapped adapter %T is not a contentsource.PagedSource — the F5 capability was erased", wrapped)
	}
	refs, _, complete, err := paged.ListPage(context.Background(), "", 100, 1<<20)
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "viaListPage" {
		t.Fatalf("ListPage did not delegate through the chain: %+v", refs)
	}
	if complete {
		t.Errorf("per-call complete=false did not survive wrapSDKContentSource+wrapContentSourceMode")
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

type fakeSDKContentSource struct{}

func (fakeSDKContentSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.content", Type: sdk.TypeContentSource, APIVersion: sdk.APIVersion}
}
func (fakeSDKContentSource) Open(context.Context, sdk.Config) error { return nil }
func (fakeSDKContentSource) List(context.Context, string) ([]sdk.DocRef, string, error) {
	return nil, "", nil
}
func (fakeSDKContentSource) Fetch(context.Context, string) (sdk.Document, error) {
	return sdk.Document{}, nil
}
func (fakeSDKContentSource) Close(context.Context) error { return nil }

type fakeSDKDeltaContentSource struct {
	fakeSDKContentSource
}

func (fakeSDKDeltaContentSource) DeltaList(context.Context, string) (sdk.DeltaPage, error) {
	return sdk.DeltaPage{}, nil
}
func (fakeSDKDeltaContentSource) FetchACL(context.Context, string) (sdk.ACLResult, error) {
	return sdk.ACLResult{}, nil
}

func TestContentSourcePluginAdapterLiveTypeHonesty(t *testing.T) {
	base := &trackedContentSourcePlugin{src: fakeSDKContentSource{}}
	if _, ok := any(base).(sdk.DeltaContentSource); ok {
		t.Fatal("plain tracked content-source plugin must not satisfy sdk.DeltaContentSource")
	}

	live := &trackedDeltaContentSourcePlugin{
		trackedContentSourcePlugin: &trackedContentSourcePlugin{src: fakeSDKDeltaContentSource{}},
		live:                       fakeSDKDeltaContentSource{},
	}
	if _, ok := any(live).(sdk.DeltaContentSource); !ok {
		t.Fatal("delta-declared tracked content-source plugin must satisfy sdk.DeltaContentSource")
	}
}

// fakeSDKPagedContentSource is an sdk.PagedContentSource returning a fixed per-call complete.
type fakeSDKPagedContentSource struct {
	fakeSDKContentSource
	complete bool
	gotItems int
	gotBytes int
}

func (s *fakeSDKPagedContentSource) ListPage(_ context.Context, _ string, maxItems, maxBytes int) ([]sdk.DocRef, string, bool, error) {
	s.gotItems, s.gotBytes = maxItems, maxBytes
	return []sdk.DocRef{{DocID: "viaListPage"}}, "next-cur", s.complete, nil
}

// TestTrackedContentSourceForwardsListPage is the F5 re-review finding-C regression: the
// tracked wrapper must NOT erase the client's bounded-pagination capability, or wrapSDKContent
// Source's sdk.PagedContentSource assertion fails and the whole F5 fix is inert. It asserts the
// tracked wrapper satisfies sdk.PagedContentSource and forwards ceilings + the per-call complete.
func TestTrackedContentSourceForwardsListPage(t *testing.T) {
	fake := &fakeSDKPagedContentSource{complete: false}
	base := &trackedContentSourcePlugin{src: fake}

	paged, ok := any(base).(sdk.PagedContentSource)
	if !ok {
		t.Fatalf("tracked content-source plugin %T does not satisfy sdk.PagedContentSource — capability erased", base)
	}
	refs, next, complete, err := paged.ListPage(context.Background(), "", 123, 456)
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "viaListPage" || next != "next-cur" {
		t.Fatalf("ListPage did not delegate to the client: refs=%+v next=%q", refs, next)
	}
	if complete {
		t.Errorf("per-call complete=false did not survive the tracked wrapper")
	}
	if fake.gotItems != 123 || fake.gotBytes != 456 {
		t.Errorf("ceilings not forwarded: items=%d bytes=%d", fake.gotItems, fake.gotBytes)
	}

	// The delta wrapper embeds the base, so it inherits ListPage too.
	live := &trackedDeltaContentSourcePlugin{trackedContentSourcePlugin: base, live: fakeSDKDeltaContentSource{}}
	if _, ok := any(live).(sdk.PagedContentSource); !ok {
		t.Fatal("delta tracked content-source plugin must also satisfy sdk.PagedContentSource")
	}
}

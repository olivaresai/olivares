// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package plugin_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/plugin"
)

// bigBytesSource streams n refs, each with a large title, in ONE page (next==""), ignoring
// the host byte ceiling — a hostile/naive external source. The BYTE ceiling is the real RAM
// guard; the host must cap by bytes and report the page incomplete (F5).
type bigBytesSource struct {
	n         int
	titleSize int
}

func (bigBytesSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.bigbytes", Type: sdk.TypeContentSource, APIVersion: sdk.APIVersion, Surfaces: []string{"knowledge.document"}}
}
func (bigBytesSource) Open(context.Context, sdk.Config) error { return nil }
func (bigBytesSource) Close(context.Context) error            { return nil }
func (bigBytesSource) Fetch(_ context.Context, id string) (sdk.Document, error) {
	return sdk.Document{Source: "big", DocID: id, Title: id, Body: []byte("b")}, nil
}
func (s bigBytesSource) List(_ context.Context, cursor string) ([]sdk.DocRef, string, error) {
	if cursor != "" {
		return nil, "", nil
	}
	title := strings.Repeat("x", s.titleSize)
	refs := make([]sdk.DocRef, s.n)
	for i := range refs {
		refs[i] = sdk.DocRef{DocID: fmt.Sprintf("doc-%d", i), Title: title}
	}
	return refs, "", nil // one giant-BYTES page, no ceiling honored
}

// largeCountSource returns a single LARGE-COUNT but small-byte page WITH a resume cursor — a
// well-behaved paginating source whose natural page exceeds the requested maxItems. The host
// must NOT falsely truncate it (F5 review finding: item count is not the RAM guard).
type largeCountSource struct{ pageSize, total int }

func (largeCountSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.largecount", Type: sdk.TypeContentSource, APIVersion: sdk.APIVersion, Surfaces: []string{"knowledge.document"}}
}
func (largeCountSource) Open(context.Context, sdk.Config) error { return nil }
func (largeCountSource) Close(context.Context) error            { return nil }
func (largeCountSource) Fetch(_ context.Context, id string) (sdk.Document, error) {
	return sdk.Document{Source: "lc", DocID: id, Title: id, Body: []byte("b")}, nil
}
func (s largeCountSource) List(_ context.Context, cursor string) ([]sdk.DocRef, string, error) {
	off := 0
	if cursor != "" {
		fmt.Sscanf(cursor, "%d", &off)
	}
	end := off + s.pageSize
	if end > s.total {
		end = s.total
	}
	refs := make([]sdk.DocRef, 0, end-off)
	for i := off; i < end; i++ {
		refs = append(refs, sdk.DocRef{DocID: fmt.Sprintf("doc-%d", i), Title: "t"})
	}
	next := ""
	if end < s.total {
		next = fmt.Sprintf("%d", end)
	}
	return refs, next, nil
}

func dispensePaged(t *testing.T, impl sdk.ContentSource) (sdk.PagedContentSource, func()) {
	t.Helper()
	client, _ := goplugin.TestPluginGRPCConn(t, false, map[string]goplugin.Plugin{
		plugin.ContentSourcePluginName: &plugin.ContentSourcePlugin{Impl: impl},
	})
	raw, err := client.Dispense(plugin.ContentSourcePluginName)
	if err != nil {
		client.Close()
		t.Fatalf("dispense: %v", err)
	}
	paged, ok := raw.(sdk.PagedContentSource)
	if !ok {
		client.Close()
		t.Fatalf("dispensed client %T is not a sdk.PagedContentSource", raw)
	}
	return paged, func() { _ = client.Close() }
}

// TestContentWireByteCeilingBounds is the F5 regression: a source that streams a huge
// BYTES page (ignoring the ceiling) must NOT be buffered into host RAM. The host caps by
// bytes and reports the page incomplete (complete=false, no resume cursor) so orphan deletion
// is withheld.
func TestContentWireByteCeilingBounds(t *testing.T) {
	const n, titleSize = 4000, 4096 // ~16 MiB streamed
	paged, closeConn := dispensePaged(t, bigBytesSource{n: n, titleSize: titleSize})
	defer closeConn()

	const maxBytes = 1 << 20 // 1 MiB request → 2 MiB hard ceiling
	refs, next, complete, err := paged.ListPage(context.Background(), "", 0, maxBytes)
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if len(refs) >= n {
		t.Fatalf("host buffered the whole %d-ref (~16MiB) page — the wire drained (F5); got %d", n, len(refs))
	}
	// ~2 MiB / ~4 KiB per ref ⇒ well under n; prove it stopped far short.
	if len(refs) > n/2 {
		t.Fatalf("host read %d of %d refs — byte ceiling did not bound RAM", len(refs), n)
	}
	if complete {
		t.Errorf("a byte-truncated page must report complete=false (deletes withheld)")
	}
	if next != "" {
		t.Errorf("a truncated page must carry no resume cursor, got %q", next)
	}
}

// TestContentWireLargeCountPageNotTruncated is the finding-2 regression: a well-behaved
// source whose single page has MANY refs (but small bytes) and a valid resume cursor must be
// read in full and reported complete — item count is not the RAM guard, so it must not
// trigger a false truncation that would silently under-sync a large source.
func TestContentWireLargeCountPageNotTruncated(t *testing.T) {
	const pageSize, total = 5000, 20000 // pages of 5000, far above the host maxItems=1000
	paged, closeConn := dispensePaged(t, largeCountSource{pageSize: pageSize, total: total})
	defer closeConn()

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 100 {
			t.Fatal("paging did not terminate")
		}
		refs, next, complete, err := paged.ListPage(context.Background(), cursor, 1000, 8<<20)
		if err != nil {
			t.Fatalf("ListPage: %v", err)
		}
		if !complete {
			t.Fatalf("a well-behaved %d-ref page (small bytes) was falsely truncated (finding 2): got %d refs, next=%q", pageSize, len(refs), next)
		}
		for _, r := range refs {
			seen[r.DocID] = true
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != total {
		t.Fatalf("large-count paging lost refs: saw %d distinct, want %d", len(seen), total)
	}
}

// TestContentWirePagesResumably proves small bounded pages enumerate to completeness.
func TestContentWirePagesResumably(t *testing.T) {
	const total = 250
	paged, closeConn := dispensePaged(t, largeCountSource{pageSize: 50, total: total})
	defer closeConn()

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 100 {
			t.Fatal("paging did not terminate")
		}
		refs, next, complete, err := paged.ListPage(context.Background(), cursor, 100, 8<<20)
		if err != nil {
			t.Fatalf("ListPage: %v", err)
		}
		if !complete {
			t.Fatalf("a bounded page must be complete: got %d refs, next=%q", len(refs), next)
		}
		for _, r := range refs {
			seen[r.DocID] = true
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != total {
		t.Fatalf("resumable paging lost refs: saw %d distinct, want %d", len(seen), total)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package contentsource

import "context"

// PagedSource extends Source with BOUNDED pagination. ListPage returns one page of
// document refs subject to explicit per-page ceilings (maxItems, maxBytes) plus an opaque
// continuation cursor ("" when exhausted; cursor=="" starts from the beginning). A source
// that implements it lets the host cap how much a single list call may return, so a
// multi-million-document upstream (SharePoint/Drive/filesystem) cannot stream its whole
// corpus into host memory in one call — the reconciliation pages through it with bounded
// RAM. The knowledge module checks for this interface at sync time; a Source that does not
// implement it falls back to the ordinary List (already page-bounded for in-tree sources).
// maxItems <= 0 or maxBytes <= 0 means "no explicit ceiling on that axis"; a source must
// still return a cursor so the caller can continue, and must never return more than
// maxItems refs when maxItems > 0.
//
// complete reports whether THIS page was fully enumerated to a resume cursor (true) or was
// cut off at a host RAM ceiling before a cursor could be produced (false). It is a PER-CALL
// result, never stored on the source, so overlapping syncs of one source instance cannot
// clobber each other's completeness verdict (F5). A consumer that reconciles deletions
// must withhold them if any page of the run reported complete==false.
type PagedSource interface {
	Source
	ListPage(ctx context.Context, cursor string, maxItems int, maxBytes int) (refs []DocRef, next string, complete bool, err error)
}

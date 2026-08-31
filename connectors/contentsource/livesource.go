// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package contentsource

import "context"

// LiveSource extends Source with live API-backed sync capabilities. A connector
// that implements LiveSource serves Source.List and Source.Fetch from the live
// upstream API in live mode; any embedded offline Store is export-mode only. It
// also supports delta-based change tracking and ACL-only refresh. DeltaList
// receives the persisted resume cursor on the first call of a pass; if a
// returned page carries NextToken, the caller passes that token back immediately
// to drain intra-pass pagination. Only ResumeToken is persisted for the next
// sync cycle. The knowledge module checks for this interface at sync time; a
// Source that does not implement it falls back to full-list reconciliation with
// orphan detection.
type LiveSource interface {
	Source
	DeltaList(ctx context.Context, sinceToken string) (DeltaPage, error)
	FetchACL(ctx context.Context, docID string) (ACLResult, error)
}

type DeltaPage struct {
	Changes []DeltaEntry
	// NextToken is strictly intra-pass pagination: non-empty means more pages
	// are available now and DeltaList must be called again with this token;
	// empty means the current delta pass is complete.
	NextToken string
	// ResumeToken is the cursor to persist for the NEXT sync cycle. A connector sets
	// it on the final page of a pass (empty on intermediate pages, and empty when the
	// connector cannot produce a fresh cursor; the engine then keeps the previous one).
	ResumeToken string
	Expired     bool
}

type DeltaEntry struct {
	DocRef     DocRef
	ChangeKind ChangeKind
}

type ChangeKind string

const (
	ChangeContent  ChangeKind = "content"
	ChangeACL      ChangeKind = "acl"
	ChangeMetadata ChangeKind = "metadata"
	ChangeDeleted  ChangeKind = "deleted"
)

type ACLResult struct {
	ACL            []string
	ExternalLabels []string
	Classification string
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"context"
	"time"
)

// SourceKind names the data source a Document came from. It is the per-source
// provenance the knowledge module records and shows. It is a plain string so an
// operator-built content connector can introduce its own source kind.
type SourceKind string

// DocRef is the lightweight listing entry a ContentSource returns from List:
// enough to decide whether to Fetch the full Document (id, label, change time),
// never the body. It carries no content and no secret.
type DocRef struct {
	// DocID is the source's stable, natural identifier for the document.
	DocID string
	// Title is a short, non-sensitive human label.
	Title string
	// ContentType is the MIME-ish type hint (e.g. "text/markdown", "text/html").
	ContentType string
	// ModifiedAt is when the source last changed the document (the connector's
	// clock, UTC). It drives incremental re-ingest and is the natural-key timestamp.
	ModifiedAt time.Time
}

// Document is one unit of knowledge content fetched from a source, carrying its
// body plus the provenance and source permissions the knowledge module needs to
// govern retrieval.
type Document struct {
	// Source is the provenance of this document.
	Source SourceKind
	// DocID is the source's natural reference (matches the DocRef.DocID).
	DocID string
	// Title is a short, non-sensitive human label.
	Title string
	// Body is the textual content to be chunked, redacted and indexed by the
	// module. It is carried as bytes on the author surface and plugin wire so the
	// transport does not impose an encoding; the host adapter converts it to the
	// in-tree contentsource.Document string body.
	Body []byte
	// ContentType is the MIME-ish type of Body (e.g. "text/markdown").
	ContentType string
	// ACL is the source's permission references for this document: group / role /
	// principal names, NEVER credential material. An empty ACL means the document
	// inherits the knowledge base's default ACL.
	ACL []string
	// Classification is the source-declared sensitivity label, or "" to inherit
	// the knowledge base's default.
	Classification string
	// SpaceRef is the source container the document lives in. Provenance only;
	// non-sensitive.
	SpaceRef string
	// ModifiedAt is when the source last changed the document (UTC).
	ModifiedAt time.Time
	// Attributes is a small map of non-sensitive provenance metadata. NEVER
	// credential material, NEVER PII the source does not need to expose.
	Attributes map[string]string
	// ExternalLabels are source-declared sensitivity labels from external
	// classification systems. Empty means no external labels declared.
	ExternalLabels []string
}

// ACLResult is the live ACL-only refresh result for one document. ACL contains
// permission references, never credential material. An empty ACL means the
// document inherits the knowledge base's default ACL.
type ACLResult struct {
	ACL            []string
	ExternalLabels []string
	Classification string
}

// ChangeKind classifies one live content-source delta.
type ChangeKind string

const (
	// CapabilityContentDelta is declared by a content-source plugin that supports
	// DeltaList and FetchACL on the plugin wire.
	CapabilityContentDelta = "content.delta"
)

const (
	// ChangeContent means the document body changed and must be fetched and re-indexed.
	ChangeContent ChangeKind = "content"
	// ChangeACL means only source permissions changed.
	ChangeACL ChangeKind = "acl"
	// ChangeMetadata means non-body metadata changed and the document should be refreshed.
	ChangeMetadata ChangeKind = "metadata"
	// ChangeDeleted means the document was removed from the upstream source.
	ChangeDeleted ChangeKind = "deleted"
)

// Change is one live content-source delta entry.
type Change struct {
	DocRef     DocRef
	ChangeKind ChangeKind
}

// DeltaPage is one page of live content-source changes. NextToken is strictly
// intra-pass pagination; ResumeToken is the cursor to persist for the next sync
// cycle; Expired means the caller must fall back to full reconciliation.
type DeltaPage struct {
	Changes     []Change
	NextToken   string
	ResumeToken string
	Expired     bool
}

// ContentSource is the author-facing content-source plugin contract. It is the
// out-of-process wire seam for governed knowledge documents and deliberately
// duplicates the in-tree connectors/contentsource.Source shape without importing
// it: plugin authors depend only on this Apache-2.0, stdlib-only SDK. The host
// adapter converts between sdk.ContentSource and connectors/contentsource.Source.
//
// Lifecycle: Open (configure once, secrets already resolved) -> List (page the
// available document refs) -> Fetch (one document body + provenance) -> Close.
// List returns refs cheap enough to enumerate and must honor ctx. Fetch is
// read-only and returns raw content; the knowledge module owns redaction.
type ContentSource interface {
	// Descriptor returns the component's stable self-description.
	Descriptor() Descriptor
	// Open prepares the source with its resolved configuration. Configuration
	// errors should be returned here.
	Open(ctx context.Context, cfg Config) error
	// List returns one page of document references plus an opaque continuation
	// cursor ("" when exhausted). cursor=="" starts from the beginning.
	List(ctx context.Context, cursor string) (refs []DocRef, next string, err error)
	// Fetch returns one document's body and provenance by its DocID.
	Fetch(ctx context.Context, docID string) (Document, error)
	// Close releases resources and must be safe to call even if Open failed.
	Close(ctx context.Context) error
}

// DeltaContentSource is the optional live-delta capability for ContentSource.
// A plugin serving this interface auto-declares the "content.delta" capability;
// hosts must not call DeltaList or FetchACL unless the capability was declared.
type DeltaContentSource interface {
	ContentSource
	DeltaList(ctx context.Context, sinceToken string) (DeltaPage, error)
	FetchACL(ctx context.Context, docID string) (ACLResult, error)
}

// PagedContentSource is the optional BOUNDED-pagination capability for ContentSource.
// ListPage returns one page of refs subject to explicit per-page ceilings (maxItems,
// maxBytes) plus the opaque resume cursor. The host-side plugin client implements it so an
// external content source cannot stream its whole corpus into host RAM in one call
// (F5): the host caps how much a single call may return and pages through with bounded
// memory. maxItems/maxBytes <= 0 mean "no explicit ceiling on that axis".
//
// complete is a PER-CALL result: true when the page enumerated to a resume cursor, false
// when the host had to cut it off at its RAM ceiling before a cursor was produced (a source
// that ignored the byte ceiling). It is returned, never stashed on the source, so
// concurrent syncs of one source instance cannot clobber each other's verdict; the caller
// withholds orphan deletion for the whole run if any page reported false.
type PagedContentSource interface {
	ContentSource
	ListPage(ctx context.Context, cursor string, maxItems int, maxBytes int) (refs []DocRef, next string, complete bool, err error)
}

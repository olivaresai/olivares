// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package contentsource

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// SourceKind names the data source a Document came from. It is the per-source
// provenance the knowledge module records and shows so governance can tell a
// Confluence page from a Notion doc from an S3 object. It is a plain string so an
// operator-built content connector can introduce its own.
type SourceKind string

// The seeded data sources.
const (
	// SourceGDrive is Google Drive (Docs/Sheets/Slides/files).
	SourceGDrive SourceKind = "gdrive"
	// SourceConfluence is Atlassian Confluence (spaces and pages).
	SourceConfluence SourceKind = "confluence"
	// SourceNotion is Notion (workspaces, databases and pages).
	SourceNotion SourceKind = "notion"
	// SourceSharePoint is Microsoft SharePoint / OneDrive (sites and documents).
	SourceSharePoint SourceKind = "sharepoint"
	// SourceS3 is object storage content (S3 / R2 / GCS objects).
	SourceS3 SourceKind = "s3"
	// SourceSAPOData is SAP system content via OData v4 (materials, orders, attachments).
	SourceSAPOData SourceKind = "sap_odata"
	// SourceSalesforce is Salesforce CRM content via REST API (accounts, cases, knowledge articles).
	SourceSalesforce SourceKind = "salesforce"
	// SourceSnowflake is Snowflake data warehouse content (tables, views).
	SourceSnowflake SourceKind = "snowflake"
	// SourceAzureAISearch is Azure AI Search indexed documents.
	SourceAzureAISearch SourceKind = "azure_ai_search"
)

// ContentClass is the broad class of what a source ingests. It is the explicit
// boundary marker that keeps a knowledge content connector (documents) from being
// confused with an data-store audit feed (R/RW access logs) or an
// inventory feed — a content source declares ClassDocument and nothing else.
type ContentClass string

// The content classes. Only ClassDocument is ingested for knowledge; the others
// exist so Source.Kind can REJECT a non-document feed rather than silently ingest
// audit logs as if they were knowledge (the boundary, docs/SECURITY-HARDENING.md minimal data).
const (
	// ClassDocument is human/agent knowledge content (a doc, a page, an object).
	ClassDocument ContentClass = "document"
	// ClassAuditLog is a data-store R/RW audit record — NOT knowledge (owns it).
	ClassAuditLog ContentClass = "audit_log"
	// ClassInventory is a runtime/cloud inventory record — NOT knowledge (owns it).
	ClassInventory ContentClass = "inventory"
)

// DocRef is the lightweight listing entry a Source returns from List: enough to
// decide whether to Fetch the full Document (id, label, change time), never the
// body. It carries no content and no secret.
type DocRef struct {
	// DocID is the source's stable, natural identifier for the document. It is the
	// de-duplication key the knowledge module persists as source_doc_id.
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
// body PLUS the provenance and source permissions the knowledge module needs to
// govern retrieval. The body MAY contain raw secrets the connector did not strip
// (see the package doc) — the module redacts before persisting.
type Document struct {
	// Source is the provenance of this document.
	Source SourceKind
	// DocID is the source's natural reference (matches the DocRef.DocID).
	DocID string
	// Title is a short, non-sensitive human label.
	Title string
	// Body is the textual content to be chunked, redacted and indexed by the
	// module. It is NOT scrubbed by the connector (the module owns redaction).
	Body string
	// ContentType is the MIME-ish type of Body (e.g. "text/markdown").
	ContentType string
	// ACL is the source's permission references for this document: group / role /
	// principal names (e.g. "group:engineering", "space:ENG"), NEVER credential
	// material. An empty ACL means the document inherits the knowledge base's
	// default ACL. The module intersects these with the requesting identity's
	// groups to govern retrieval (chunk-level, not just UI).
	ACL []string
	// Classification is the source-declared sensitivity label (e.g. "public",
	// "internal", "confidential"), or "" to inherit the knowledge base's default.
	Classification string
	// SpaceRef is the source container the document lives in (a Drive folder, a
	// Confluence space, a Notion database, a SharePoint site, an S3 bucket/prefix).
	// Provenance only; non-sensitive.
	SpaceRef string
	// ModifiedAt is when the source last changed the document (UTC).
	ModifiedAt time.Time
	// Attributes is a small map of non-sensitive provenance metadata (e.g.
	// "author_label", "url_path"). NEVER credential material, NEVER PII the source
	// does not need to expose.
	Attributes map[string]string
	// ExternalLabels are source-declared sensitivity labels from external
	// classification systems (e.g. "purview:highly-confidential", "uc:pii",
	// "gdrive:restricted"). Additive to Classification — retrieval enforces
	// both axes deny-closed. Empty means no external labels declared.
	ExternalLabels []string
}

// Source is implemented by an in-tree knowledge DATA connector. It is pull-driven
// by the knowledge module: Open (configure once, secrets by reference) → List
// (page the available documents) → Fetch (one document's body + provenance) →
// Close. It is READ-ONLY on the source; it never writes back and never mutates
// the source. This engine-driven interface is deliberately duplicated by the
// author-facing sdk.ContentSource plugin surface; the host adapter converts
// between them so external authors never import connectors/ or core/.
//
// A Source is NOT an sdk.SourceConnector and emits no observations: the module
// owns the ingest lifecycle, drives Open/List/Fetch/Close itself (NOT the runtime
// scheduler), and is responsible for cancellation via ctx, timeouts, partial-
// failure cleanup and any FindingReport it emits on the bus.
type Source interface {
	// Descriptor returns the connector's stable self-description and its config
	// schema (including secret-by-reference fields, docs/SECURITY-HARDENING.md).
	Descriptor() sdk.Descriptor
	// Kind returns the content class this source ingests. The knowledge module
	// rejects any source whose Kind is not ClassDocument (the boundary).
	Kind() ContentClass
	// Open prepares the connector with its resolved configuration. It is called
	// once before List/Fetch. A configuration error (missing required setting,
	// unreachable target) is returned here. A connector with no credential
	// configured opens successfully and behaves as an empty source (List returns
	// nothing) — declared offline, never a hard failure.
	Open(ctx context.Context, cfg sdk.Config) error
	// List returns one page of document references from the source, plus an opaque
	// continuation cursor ("" when exhausted). It honors ctx for cancellation and
	// is read-only. cursor=="" starts from the beginning.
	List(ctx context.Context, cursor string) (refs []DocRef, next string, err error)
	// Fetch returns one document's body and provenance by its DocID. It is
	// read-only and honors ctx. The returned Body is raw (the module redacts it);
	// the connector must NOT log, cache or replay it.
	Fetch(ctx context.Context, docID string) (Document, error)
	// Close releases the connector's resources. It is called once, after the last
	// Fetch, and must be safe to call even if Open failed.
	Close(ctx context.Context) error
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pgcontent

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/internal/content"
)

// SourcePostgres is the provenance SourceKind this connector stamps on every
// Document (contentsource.SourceKind is an open string so an operator connector may
// introduce its own).
const SourcePostgres contentsource.SourceKind = "postgres"

// row is one materialized database row as string-valued columns. The live path
// stringifies pgx values into this shape and the export path unmarshals into it, so
// document mapping is identical for both modes.
type row map[string]string

// docID builds the stable, source-natural identifier for a row from its key columns:
// "<container>#<esc(key1)>/<esc(key2)>". Each key value is query-escaped so a value
// containing '/', ':' or '#' can never corrupt the id or its decoding (decodeKeys is
// the inverse, used by the live Fetch to rebuild the WHERE from a DocID). It is the
// de-duplication key the knowledge module persists as source_doc_id, so it must be
// stable across syncs.
func (sc *sourceConfig) docID(r row) string {
	parts := make([]string, 0, len(sc.keyColumns))
	for _, c := range sc.keyColumns {
		parts = append(parts, url.QueryEscape(r[c]))
	}
	id := sc.container() + "#" + strings.Join(parts, "/")
	return content.Truncate(id, content.MaxRefLen)
}

// decodeKeys is the inverse of docID: it recovers the key-column values (in
// keyColumns order) from a DocID. It returns an error when the id does not carry the
// expected number of key parts (e.g. a truncated id for a pathologically long key),
// so the live Fetch fails honestly rather than building a wrong WHERE.
func (sc *sourceConfig) decodeKeys(docID string) ([]string, error) {
	i := strings.LastIndexByte(docID, '#')
	if i < 0 {
		return nil, fmt.Errorf("pgcontent: malformed docID %q", docID)
	}
	parts := strings.Split(docID[i+1:], "/")
	if len(parts) != len(sc.keyColumns) {
		return nil, fmt.Errorf("pgcontent: docID %q has %d key parts, want %d", docID, len(parts), len(sc.keyColumns))
	}
	out := make([]string, len(parts))
	for j, p := range parts {
		v, err := url.QueryUnescape(p)
		if err != nil {
			return nil, fmt.Errorf("pgcontent: malformed key in docID %q: %w", docID, err)
		}
		out[j] = v
	}
	return out, nil
}

// container is the logical source container label (SpaceRef / DocID prefix).
func (sc *sourceConfig) container() string {
	if sc.spaceRef != "" {
		return sc.spaceRef
	}
	if sc.table != "" {
		return "postgres:" + sc.schema + "." + sc.table
	}
	return "postgres:" + sc.schema + ".query"
}

// body concatenates the configured body columns. A single body column is emitted
// verbatim; multiple are labeled "<column>: <value>" so the retrieved chunk keeps
// which field it came from. The connector does NOT redact — the module does.
func (sc *sourceConfig) body(r row) string {
	if len(sc.bodyColumns) == 1 {
		return content.Truncate(r[sc.bodyColumns[0]], content.MaxBodyBytes)
	}
	var b strings.Builder
	for _, c := range sc.bodyColumns {
		v := r[c]
		if v == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(c)
		b.WriteString(": ")
		b.WriteString(v)
	}
	return content.Truncate(b.String(), content.MaxBodyBytes)
}

// acl maps the declared ACL columns to permission references (aclPrefix + value),
// e.g. an "owner_group" column with value "eng" → "group:eng". It maps ONLY what the
// row expresses — it never invents a per-row ACL the source does not carry, and it
// omits empty values. When no ACL columns are declared the Document inherits the
// knowledge base's default ACL (an empty slice), which retrieval enforces.
func (sc *sourceConfig) acl(r row) []string {
	if len(sc.aclColumns) == 0 {
		return nil
	}
	refs := make([]string, 0, len(sc.aclColumns))
	for _, c := range sc.aclColumns {
		v := strings.TrimSpace(r[c])
		if v == "" {
			continue
		}
		refs = append(refs, sc.aclPrefix+v)
	}
	return content.CleanACL(refs)
}

// externalLabels adds one external sensitivity label per declared sensitive column
// that carries a value in this row: "<label>:<column>" (e.g. "pii:ssn"). This is the
// per-column classification that feeds the retrieval DLP — additive to
// the row's Classification, enforced deny-closed alongside it.
func (sc *sourceConfig) externalLabels(r row) []string {
	if len(sc.sensitiveCol) == 0 {
		return nil
	}
	var out []string
	for _, c := range sc.sensitiveCol {
		if strings.TrimSpace(r[c]) != "" {
			out = append(out, sc.sensitiveLbl+":"+c)
		}
	}
	return out
}

// attributes collects the declared metadata columns as non-sensitive provenance,
// plus the schema/table for lineage.
func (sc *sourceConfig) attributes(r row) map[string]string {
	attrs := map[string]string{"schema": sc.schema}
	if sc.table != "" {
		attrs["table"] = sc.table
	}
	for _, c := range sc.metadataCol {
		if v := strings.TrimSpace(r[c]); v != "" {
			attrs[c] = content.Truncate(v, content.MaxRefLen)
		}
	}
	return attrs
}

// title resolves the document title from the title column, falling back to the DocID.
func (sc *sourceConfig) title(r row, docID string) string {
	if sc.titleColumn != "" {
		if v := strings.TrimSpace(r[sc.titleColumn]); v != "" {
			return content.Truncate(v, content.MaxTitleLen)
		}
	}
	return content.Truncate(docID, content.MaxTitleLen)
}

// modifiedAt parses the updated-at column as a timestamp (RFC3339 or a common
// Postgres timestamp layout), returning the zero time when absent or unparseable.
func (sc *sourceConfig) modifiedAt(r row) time.Time {
	if sc.updatedAtCol == "" {
		return time.Time{}
	}
	return parseTimestamp(r[sc.updatedAtCol])
}

// toDocument maps one row to a knowledge Document using the declarative config.
func (sc *sourceConfig) toDocument(r row) contentsource.Document {
	id := sc.docID(r)
	return contentsource.Document{
		Source:         SourcePostgres,
		DocID:          id,
		Title:          sc.title(r, id),
		Body:           sc.body(r),
		ContentType:    sc.contentType,
		ACL:            sc.acl(r),
		Classification: strings.TrimSpace(r[sc.classColumn]),
		SpaceRef:       sc.container(),
		ModifiedAt:     sc.modifiedAt(r),
		Attributes:     sc.attributes(r),
		ExternalLabels: sc.externalLabels(r),
	}
}

// docRef is the lightweight listing entry for a row.
func (sc *sourceConfig) docRef(r row) contentsource.DocRef {
	id := sc.docID(r)
	return contentsource.DocRef{
		DocID:       id,
		Title:       sc.title(r, id),
		ContentType: sc.contentType,
		ModifiedAt:  sc.modifiedAt(r),
	}
}

// timestampLayouts are the timestamp formats the updated-at cursor may arrive in
// (export JSON strings; the live path formats pgx time.Time as RFC3339Nano).
var timestampLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999-07",
	"2006-01-02 15:04:05.999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func parseTimestamp(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

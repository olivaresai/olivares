// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package contentsource

import (
	"context"
	"errors"
	"fmt"
)

// RichDocKind is the coarse family a content connector sniffs a file's bytes into
// before deciding whether to hand it to a RichDocExtractor. The connector only reads
// a few magic bytes (RichDocOOXML is a CANDIDATE — the authoritative validation that
// it really is an Office Open XML package happens inside the sandboxed extractor, so
// no hostile-archive parsing runs in the trusted engine process).
type RichDocKind string

const (
	// RichDocNone is the default: not a recognized rich document (the connector's
	// ordinary text / binary-skip path applies).
	RichDocNone RichDocKind = ""
	// RichDocOOXML is a zip-container candidate (DOCX/PPTX/XLSX). The extractor
	// resolves the exact subtype and refuses a zip that is not really OOXML.
	RichDocOOXML RichDocKind = "ooxml"
	// RichDocPDF is a PDF (magic %PDF-). PDF extraction is a separate, gated
	// dependency decision; a connector with no PDF-capable extractor skips it,
	// honestly counted.
	RichDocPDF RichDocKind = "pdf"
)

// ErrNotExtractable is returned by RichDocExtractor.Extract for input the extractor
// classified as not-extractable — a zip that is not real OOXML, a disabled kind
// (PDF today), or a document that is malformed / exceeds the extractor's safety
// limits. It is a SKIP signal, not a failure: the caller counts an honest
// "detected, not ingested" rather than aborting the whole ingest. It wraps
// ErrSkipDocument so a Source.Fetch that surfaces it lets the ingest loop skip.
var ErrNotExtractable = fmt.Errorf("contentsource: document is not extractable: %w", ErrSkipDocument)

// ErrSkipDocument marks a Source.Fetch result that the ingest loop must SKIP (count
// as detected-but-not-ingested) rather than treat as a fatal error that aborts the
// whole sync/ingest. It is for DETERMINISTIC per-document non-ingestability — the
// bytes are not text, not an extractable rich document, or over a hard limit — NOT
// for a transient/operational failure (a network or auth error, which must still
// abort so a source blip is never mistaken for "every document deleted"). A
// connector returns it (usually wrapped) from Fetch; the knowledge module tests it
// with errors.Is and continues to the next document.
var ErrSkipDocument = errors.New("contentsource: skip this document")

// RichDocExtractor turns the raw bytes of a sniffed rich document into plain text.
// The in-tree implementation runs an OUT-OF-PROCESS, resource-bounded subprocess (the
// engine's `__extract` helper under plugin confinement). Environment SCOPING always
// applies — the child inherits none of the engine's environment, so no engine secret
// is passed to it. The strength of the rest of the isolation depends on the host:
// when the engine is privileged the child also gets a dedicated non-root uid (a
// hostile child then cannot read the engine's memory/environment); on an unprivileged
// engine the child runs at the engine's own uid and that OS boundary is NOT present —
// the safety then rests on the parser being memory-safe, dependency-free stdlib code
// (see the engine's PLUGIN-CONFINEMENT threat model and RICHDOC-EXTRACTION notes). A
// content connector receives an implementation by injection (it never builds one
// itself — the connectors module must not import the engine); a connector with a nil
// extractor simply falls back to its non-rich behavior.
//
// Extract returns the extracted text and the resolved provenance subtype (e.g.
// "docx", "pptx", "xlsx"). It returns ErrNotExtractable for a classified skip and a
// wrapped error only for an unexpected operational failure (the caller may then fail
// the single document without inventing content). raw is not retained.
type RichDocExtractor interface {
	Extract(ctx context.Context, kind RichDocKind, raw []byte) (text string, subtype string, err error)
}

// SniffRichDoc classifies a file by its leading magic bytes only — it never parses
// the container, so it is safe to run in-process on untrusted input. It recognizes
// the OOXML zip local-file-header and the PDF header; everything else is RichDocNone.
func SniffRichDoc(raw []byte) RichDocKind {
	if len(raw) >= 5 && raw[0] == '%' && raw[1] == 'P' && raw[2] == 'D' && raw[3] == 'F' && raw[4] == '-' {
		return RichDocPDF
	}
	// A real OOXML package begins with a zip LOCAL file header (PK\x03\x04). The
	// empty-archive (PK\x05\x06) and spanned (PK\x07\x08) markers are not OOXML.
	if len(raw) >= 4 && raw[0] == 'P' && raw[1] == 'K' && raw[2] == 0x03 && raw[3] == 0x04 {
		return RichDocOOXML
	}
	return RichDocNone
}

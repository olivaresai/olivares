// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package fscontent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/internal/content"
)

// SourceFilesystem is the provenance SourceKind stamped on every Document.
const SourceFilesystem contentsource.SourceKind = "filesystem"

// sniffLen is how many leading bytes are read to classify a file (magic bytes only).
const sniffLen = 512

// richDocMaxInputBytes caps the whole-file read for a rich-document candidate — the
// extractor needs the COMPLETE archive (not the text-body truncation), so a rich doc
// is read up to this bound (matching the extractor's own input ceiling) and a larger
// one is an honest skip rather than a truncated, un-parseable archive.
const richDocMaxInputBytes = 25 << 20 // 25 MiB

// Per-document skip sentinels. Each wraps contentsource.ErrSkipDocument so the
// knowledge ingest loop counts an honest "detected, not ingested" and moves on,
// instead of aborting the whole sync (these are deterministic per-file conditions,
// never a transient source failure).
var (
	// errBinaryContent: the bytes are not text (a NUL appeared) and it is not a
	// recognized rich document — an honest "declared, not ingested" case.
	errBinaryContent = fmt.Errorf("fscontent: file content is not text: %w", contentsource.ErrSkipDocument)
	// errRichDocUnsupported: a rich document was detected but no extractor can handle
	// it here (a PDF this release, or OOXML with no extractor injected).
	errRichDocUnsupported = fmt.Errorf("fscontent: rich document detected but extraction not enabled: %w", contentsource.ErrSkipDocument)
	// errRichDocTooLarge: a rich-document candidate exceeds the extractor's input cap;
	// a truncated archive cannot be parsed, so it is skipped rather than mis-read.
	errRichDocTooLarge = fmt.Errorf("fscontent: rich document exceeds the extraction input cap: %w", contentsource.ErrSkipDocument)
)

// posixMeta is the platform-derived governance metadata for one file: the mapped ACL,
// the classification and external labels, and the non-sensitive provenance attributes.
// The Linux build fills owner/group/mode + POSIX ACL + xattrs; other platforms fill
// only what os.FileInfo exposes (declared, never invented).
type posixMeta struct {
	acl            []string
	classification string
	labels         []string
	attrs          map[string]string
}

// container is the logical source container label (SpaceRef).
func (sc *sourceConfig) container() string {
	if sc.spaceRef != "" {
		return sc.spaceRef
	}
	return "file:" + sc.root
}

// readDocument reads one file's body through the confined root and assembles the
// governed Document. It is read-only, size-bounded, and refuses non-text content. A
// rich document (OOXML) is sniffed by its leading bytes and, when an extractor was
// injected, extracted out-of-process into text; a rich document that cannot be
// extracted (disabled, over the input cap, or not really OOXML) is an honest skip
// (contentsource.ErrSkipDocument), never a truncated-binary body.
func readDocument(ctx context.Context, root *os.Root, sc *sourceConfig, fe fileEntry) (contentsource.Document, error) {
	f, err := root.Open(fe.rel)
	if err != nil {
		return contentsource.Document{}, err
	}
	defer func() { _ = f.Close() }()

	// Sniff a small prefix so a large binary is classified without reading it whole.
	// The content sniff only REROUTES a file whose extension is a rich-doc candidate:
	// a plain text/markdown/csv file whose bytes happen to begin with PK\x03\x04 or
	// %PDF- must stay on the text path (it was ingested as text before extraction
	// existed), not be demoted to a magic-byte skip.
	richCandidate := isRichDocPath(fe.title)
	var kind contentsource.RichDocKind
	if richCandidate {
		prefix := make([]byte, sniffLen)
		n, err := io.ReadFull(f, prefix)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return contentsource.Document{}, err
		}
		kind = contentsource.SniffRichDoc(prefix[:n])
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return contentsource.Document{}, err
		}
	}

	switch {
	case kind == contentsource.RichDocOOXML && sc.extractRichDocs:
		return readExtractedDocument(ctx, f, sc, fe, kind)
	case kind == contentsource.RichDocOOXML || kind == contentsource.RichDocPDF:
		// A rich document we cannot extract here (PDF this release, or OOXML with no
		// extractor injected): a counted skip, not a binary body.
		return contentsource.Document{}, errRichDocUnsupported
	}

	// Ordinary text path.
	raw, truncated, err := readBody(f, sc.maxFileBytes)
	if err != nil {
		return contentsource.Document{}, err
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return contentsource.Document{}, errBinaryContent
	}
	// A rich-doc-extensioned file that did not sniff as OOXML is plain text — do not
	// stamp the extension-derived Office MIME on a text body (honest ContentType).
	contentType := fe.contentType
	if richCandidate {
		contentType = "text/plain"
	}
	return assembleDocument(f, sc, fe, string(raw), contentType, truncated, nil), nil
}

// readExtractedDocument reads the FULL rich-document archive (bounded by the
// extractor's input cap) and hands its bytes to the out-of-process sandboxed
// extractor. The extracted text becomes the body; provenance records the original
// document subtype. A not-extractable / over-cap file is a classified skip.
func readExtractedDocument(ctx context.Context, f *os.File, sc *sourceConfig, fe fileEntry, kind contentsource.RichDocKind) (contentsource.Document, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return contentsource.Document{}, err
	}
	raw, truncated, err := readBody(f, richDocMaxInputBytes)
	if err != nil {
		return contentsource.Document{}, err
	}
	if truncated {
		return contentsource.Document{}, errRichDocTooLarge
	}
	text, subtype, err := sc.extractor.Extract(ctx, kind, raw)
	if err != nil {
		// A classified skip (not-OOXML / malformed / over-limit) is surfaced as a
		// skip; an unexpected operational failure is returned as-is so it is visible.
		return contentsource.Document{}, err
	}
	extra := map[string]string{"extracted": "true"}
	if subtype != "" {
		extra["source_content_type"] = subtype
	}
	// The body is now plain text; ContentType reflects the body, not the source file.
	return assembleDocument(f, sc, fe, text, "text/plain", false, extra), nil
}

// assembleDocument builds the governed Document from a resolved body plus the file's
// platform metadata. extra carries any additional non-sensitive provenance attributes
// (e.g. the extracted source subtype); it is merged into the attribute map.
func assembleDocument(f *os.File, sc *sourceConfig, fe fileEntry, body, contentType string, truncated bool, extra map[string]string) contentsource.Document {
	pm := platformMeta(f, sc)
	attrs := pm.attrs
	if attrs == nil {
		attrs = map[string]string{}
	}
	attrs["path"] = fe.rel
	if truncated {
		attrs["truncated"] = "true"
	}
	for k, v := range extra {
		attrs[k] = v
	}

	classification := pm.classification
	if classification == "" {
		classification = sc.classifyDefault
	}

	return contentsource.Document{
		Source:         SourceFilesystem,
		DocID:          fe.rel,
		Title:          content.Truncate(fe.title, content.MaxTitleLen),
		Body:           content.Truncate(body, content.MaxBodyBytes),
		ContentType:    contentType,
		ACL:            content.CleanACL(pm.acl),
		Classification: classification,
		SpaceRef:       sc.container(),
		ModifiedAt:     fe.modTime,
		Attributes:     attrs,
		ExternalLabels: pm.labels,
	}
}

// readBody reads up to maxBytes of the file, reporting whether it was truncated.
func readBody(f *os.File, maxBytes int) ([]byte, bool, error) {
	raw, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		return nil, false, err
	}
	if len(raw) > maxBytes {
		return raw[:maxBytes], true, nil
	}
	return raw, false, nil
}

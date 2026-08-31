// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package richdoc extracts plain text from Office Open XML documents (DOCX, PPTX,
// XLSX) using only the Go standard library (archive/zip + encoding/xml) — no
// third-party parser is vendored, so there is no hostile-C surface and the whole
// thing is trivially auditable.
//
// It is the code that runs INSIDE the sandboxed `olivares __extract` subprocess: it
// takes the raw document bytes and returns text, and it is written to be safe on
// deliberately hostile input. Every loop is bounded (Limits) so a malformed or
// bomb-shaped archive is REFUSED, never allowed to exhaust memory or CPU:
//
//   - the input size, the entry count, each part's decompressed size, the total
//     decompressed budget and the emitted-text size are all capped;
//   - decompression is bounded by an io.LimitReader (the zip header's declared size
//     is a fast reject only — the LimitReader is the real guard against a lying
//     header / zip bomb);
//   - XML is decoded in Strict mode with a nesting-depth cap; Go's encoding/xml does
//     NOT resolve external entities or DTDs, so XXE and billion-laughs expansion are
//     not reachable here (verified: encoding/xml only expands the five predefined
//     entities and errors on any other) — the depth cap defends against pathological
//     element nesting, the one remaining unbounded XML vector.
//
// It never writes to the filesystem (so zip-slip is irrelevant — part names are
// matched, never opened as paths), never follows or executes macros / embedded OLE
// objects (only the specific text parts are read), and is deterministic (parts are
// visited in a fixed order) so the same document always yields the same text.
//
// OCR is explicitly out of scope: a document with no text layer yields empty text,
// never invented content. PDF is not handled here (a separate dependency decision).
//
// Extracted-parts scope (deliberately the primary text-bearing parts, stated so a
// caller never presents the result as a complete rendering):
//   - DOCX: the main body (word/document.xml). Headers, footers, footnotes/endnotes
//     and comments live in separate parts and are NOT extracted.
//   - PPTX: the text of every numbered slide part (ppt/slides/slideN.xml), in slide-
//     number order. Speaker notes, masters and layouts are NOT extracted. All numbered
//     slide parts are read whether or not presentation.xml references them, so hidden/
//     unreferenced slide text IS ingested (extraction favors capturing more text for
//     downstream governance over matching what a viewer renders); duplicate slide part
//     names are collapsed first-wins, matching the primary-part de-dup below.
//   - XLSX: the shared-string table (xl/sharedStrings.xml). Numeric-only cells,
//     formulas and inline (non-shared) strings are NOT rendered.
package richdoc

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Subtype is the resolved OOXML document family.
type Subtype string

const (
	// SubtypeDOCX is a WordprocessingML document (word/document.xml).
	SubtypeDOCX Subtype = "docx"
	// SubtypePPTX is a PresentationML deck (ppt/slides/slide*.xml).
	SubtypePPTX Subtype = "pptx"
	// SubtypeXLSX is a SpreadsheetML workbook (xl/sharedStrings.xml).
	SubtypeXLSX Subtype = "xlsx"
)

// Result is the extracted plain text plus the resolved provenance subtype.
type Result struct {
	Text    string
	Subtype Subtype
}

// Sentinel errors. They are classified so the caller can count an HONEST skip
// (malformed / not-OOXML / over-limit) rather than crashing or ingesting garbage.
var (
	// ErrNotOOXML: the archive is a valid zip but not a recognized OOXML document
	// (no [Content_Types].xml, or none of the known primary parts).
	ErrNotOOXML = errors.New("richdoc: not a recognized OOXML document")
	// ErrMalformed: the bytes are not a readable zip, exceed the entry cap, or the
	// XML is unparseable / pathologically nested.
	ErrMalformed = errors.New("richdoc: malformed or unsafe document")
	// ErrTooLarge: the input or a decompressed part exceeds the configured limits
	// (a zip bomb or an oversized document).
	ErrTooLarge = errors.New("richdoc: document exceeds extraction limits")
)

// Limits bound the extractor so a hostile file cannot exhaust memory or CPU. The
// zero value is NOT usable — callers build one with DefaultLimits so an extraction
// is never run unbounded by accident.
type Limits struct {
	// MaxInputBytes rejects an input archive larger than this outright.
	MaxInputBytes int64
	// MaxParts rejects an archive with more than this many entries (a central
	// directory with millions of entries is itself a resource attack).
	MaxParts int
	// MaxPartBytes caps the DECOMPRESSED size of any single part that is read.
	MaxPartBytes int64
	// MaxTotalDecompressed caps the sum of decompressed bytes across all read parts
	// (the zip-bomb budget for the whole document).
	MaxTotalDecompressed int64
	// MaxOutputBytes caps the emitted text; extraction stops once it is reached.
	MaxOutputBytes int
	// MaxDepth caps XML element nesting; a document nested deeper is refused as
	// malformed (defends against pathological nesting the decompressed-size cap
	// would not otherwise bound tightly).
	MaxDepth int
}

// DefaultLimits returns bounds sized for a governed knowledge document: generous
// enough for a real 25 MiB deck, tight enough that a bomb is refused fast.
func DefaultLimits() Limits {
	return Limits{
		MaxInputBytes:        25 << 20,  // 25 MiB compressed input
		MaxParts:             4096,      // OOXML rarely exceeds a few hundred parts
		MaxPartBytes:         32 << 20,  // 32 MiB per decompressed part
		MaxTotalDecompressed: 128 << 20, // 128 MiB total decompressed budget
		MaxOutputBytes:       8 << 20,   // 8 MiB of extracted text (caller re-truncates)
		MaxDepth:             10_000,    // deepest legitimate OOXML nesting is < 100
	}
}

// Part names OOXML mandates for each family. Matching is exact (or a fixed prefix
// for the multi-part slide case) — never a path opened on disk.
const (
	partContentTypes = "[Content_Types].xml"
	partWordDocument = "word/document.xml"
	partPPTPresent   = "ppt/presentation.xml"
	partXLWorkbook   = "xl/workbook.xml"
	partXLSharedStr  = "xl/sharedStrings.xml"
	prefixPPTSlides  = "ppt/slides/slide"
)

// ExtractOOXML parses an OOXML document from raw and returns its plain text. It is
// safe on hostile input: every step is bounded by lim and a classified sentinel
// error is returned instead of a panic or an unbounded read. raw is never retained.
func ExtractOOXML(raw []byte, lim Limits) (Result, error) {
	if int64(len(raw)) > lim.MaxInputBytes {
		return Result{}, ErrTooLarge
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return Result{}, ErrMalformed
	}
	if len(zr.File) > lim.MaxParts {
		return Result{}, ErrMalformed
	}

	// Index parts by their cleaned name. Duplicate names keep the first (a zip with
	// two entries of the same name is a known spoofing trick; we never merge them).
	// Slide parts honor the SAME first-wins de-dup so a duplicate slideN.xml cannot
	// smuggle in a second body that is concatenated and double-charged to the budget.
	byName := make(map[string]*zip.File, len(zr.File))
	var slideParts []*zip.File
	seenSlide := make(map[string]bool)
	for _, zf := range zr.File {
		name := path.Clean(zf.Name)
		if _, dup := byName[name]; !dup {
			byName[name] = zf
		}
		if isSlidePart(zf.Name) && !seenSlide[name] {
			seenSlide[name] = true
			slideParts = append(slideParts, zf)
		}
	}

	// An OOXML package is defined by its content-types part; without it this is just
	// some zip and must NOT be ingested as a document.
	if _, ok := byName[partContentTypes]; !ok {
		return Result{}, ErrNotOOXML
	}

	budget := lim.MaxTotalDecompressed
	switch {
	case has(byName, partWordDocument):
		text, err := extractWord(byName, lim, &budget)
		return Result{Text: text, Subtype: SubtypeDOCX}, err
	case has(byName, partPPTPresent):
		text, err := extractSlides(slideParts, lim, &budget)
		return Result{Text: text, Subtype: SubtypePPTX}, err
	case has(byName, partXLWorkbook):
		text, err := extractSheet(byName, lim, &budget)
		return Result{Text: text, Subtype: SubtypeXLSX}, err
	default:
		return Result{}, ErrNotOOXML
	}
}

func has(m map[string]*zip.File, name string) bool { _, ok := m[name]; return ok }

// isSlidePart reports whether name is a presentation slide part (ppt/slides/slideN.xml),
// excluding the ppt/slides/_rels and slideLayouts/slideMasters siblings.
func isSlidePart(name string) bool {
	c := path.Clean(name)
	if !strings.HasPrefix(c, prefixPPTSlides) || !strings.HasSuffix(c, ".xml") {
		return false
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(c, prefixPPTSlides), ".xml")
	if rest == "" { // "ppt/slides/slide.xml" is not a numbered slide
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// extractWord pulls text from the main document part. Paragraphs (<w:p>) become
// newlines; runs (<w:t>) are concatenated.
func extractWord(byName map[string]*zip.File, lim Limits, budget *int64) (string, error) {
	data, err := readPart(byName[partWordDocument], lim, budget)
	if err != nil {
		return "", err
	}
	return collectText(data, "t", "p", lim.MaxOutputBytes, lim.MaxDepth)
}

// extractSlides pulls text from every numbered slide, in slide-number order so the
// output is deterministic regardless of the zip's internal ordering.
func extractSlides(slides []*zip.File, lim Limits, budget *int64) (string, error) {
	sort.Slice(slides, func(i, j int) bool { return slideNum(slides[i].Name) < slideNum(slides[j].Name) })
	var b strings.Builder
	for _, zf := range slides {
		if b.Len() >= lim.MaxOutputBytes {
			break
		}
		data, err := readPart(zf, lim, budget)
		if err != nil {
			return "", err
		}
		remaining := lim.MaxOutputBytes - b.Len()
		text, err := collectText(data, "t", "p", remaining, lim.MaxDepth)
		if err != nil {
			return "", err
		}
		if text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(text)
		}
	}
	return b.String(), nil
}

// extractSheet pulls the workbook's shared-string table (the textual cell content).
// One shared string (<si>) per line. Numeric-only cells and formulas are NOT
// rendered — an honest, declared limitation of shared-string extraction.
func extractSheet(byName map[string]*zip.File, lim Limits, budget *int64) (string, error) {
	zf, ok := byName[partXLSharedStr]
	if !ok {
		return "", nil // a workbook with no shared strings has no extractable text
	}
	data, err := readPart(zf, lim, budget)
	if err != nil {
		return "", err
	}
	return collectText(data, "t", "si", lim.MaxOutputBytes, lim.MaxDepth)
}

func slideNum(name string) int {
	c := path.Clean(name)
	rest := strings.TrimSuffix(strings.TrimPrefix(c, prefixPPTSlides), ".xml")
	n, err := strconv.Atoi(rest)
	if err != nil {
		return 1 << 30 // unparseable slide numbers sort last, deterministically
	}
	return n
}

// readPart decompresses one zip entry under the per-part and total-decompressed
// budgets. The header's declared UncompressedSize64 is a fast reject; the
// io.LimitReader is the authoritative guard (a lying header cannot get past it).
func readPart(zf *zip.File, lim Limits, budget *int64) ([]byte, error) {
	if zf == nil {
		return nil, ErrNotOOXML
	}
	if zf.UncompressedSize64 > uint64(lim.MaxPartBytes) {
		return nil, ErrTooLarge
	}
	rc, err := zf.Open()
	if err != nil {
		return nil, ErrMalformed
	}
	defer func() { _ = rc.Close() }()

	limit := lim.MaxPartBytes
	if *budget < limit {
		limit = *budget
	}
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, ErrMalformed
	}
	if int64(len(data)) > limit {
		return nil, ErrTooLarge
	}
	*budget -= int64(len(data))
	return data, nil
}

// collectText walks the XML in strict mode, concatenating the character data of
// every element whose LOCAL name is textLocal, and inserting a newline whenever an
// element whose local name is paraLocal closes. It stops at maxOut bytes and refuses
// nesting deeper than maxDepth (ErrMalformed). External entities/DTDs are never
// resolved by encoding/xml, so no entity-expansion or XXE vector is reachable.
func collectText(data []byte, textLocal, paraLocal string, maxOut, maxDepth int) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	// Do NOT set dec.Entity: leaving it nil means any non-predefined entity is an
	// error rather than an expansion — closing the billion-laughs door explicitly.

	var b strings.Builder
	depth := 0
	inText := 0 // >0 while inside one or more <textLocal> elements
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", ErrMalformed
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth > maxDepth {
				return "", ErrMalformed
			}
			if t.Name.Local == textLocal {
				inText++
			}
		case xml.EndElement:
			if depth > 0 {
				depth--
			}
			if t.Name.Local == textLocal && inText > 0 {
				inText--
			}
			if t.Name.Local == paraLocal {
				if b.Len() > 0 && b.Len() < maxOut {
					b.WriteByte('\n')
				}
			}
		case xml.CharData:
			if inText > 0 && b.Len() < maxOut {
				remaining := maxOut - b.Len()
				if len(t) > remaining {
					b.Write(t[:remaining])
				} else {
					b.Write(t)
				}
			}
		}
		if b.Len() >= maxOut {
			// Output cap reached: keep draining tokens is pointless — stop cleanly.
			break
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

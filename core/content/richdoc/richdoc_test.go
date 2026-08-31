// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package richdoc

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

// makeZip builds an in-memory zip from name→content parts (deterministic caller
// order is preserved by using an ordered slice of pairs).
type part struct{ name, content string }

func makeZip(t *testing.T, parts []part) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := zw.Create(p.name)
		if err != nil {
			t.Fatalf("zip create %q: %v", p.name, err)
		}
		if _, err := w.Write([]byte(p.content)); err != nil {
			t.Fatalf("zip write %q: %v", p.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

const ctypes = `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`

func docXML(body string) string {
	return `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body + `</w:body></w:document>`
}

func TestExtractOOXML_DOCX(t *testing.T) {
	raw := makeZip(t, []part{
		{"[Content_Types].xml", ctypes},
		{"word/document.xml", docXML(`<w:p><w:r><w:t>Hello</w:t></w:r></w:p><w:p><w:r><w:t>World</w:t></w:r></w:p>`)},
	})
	res, err := ExtractOOXML(raw, DefaultLimits())
	if err != nil {
		t.Fatalf("ExtractOOXML: %v", err)
	}
	if res.Subtype != SubtypeDOCX {
		t.Errorf("subtype = %q, want docx", res.Subtype)
	}
	if res.Text != "Hello\nWorld" {
		t.Errorf("text = %q, want %q", res.Text, "Hello\nWorld")
	}
}

func TestExtractOOXML_PPTX_SlideOrderDeterministic(t *testing.T) {
	slide := func(s string) string {
		return `<?xml version="1.0"?><p:sld xmlns:p="ppt" xmlns:a="a"><a:p><a:r><a:t>` + s + `</a:t></a:r></a:p></p:sld>`
	}
	// Slides added to the zip in REVERSE order — output must still be slide-number order.
	raw := makeZip(t, []part{
		{"[Content_Types].xml", ctypes},
		{"ppt/presentation.xml", `<p:presentation/>`},
		{"ppt/slides/slide2.xml", slide("Slide Two")},
		{"ppt/slides/slide1.xml", slide("Slide One")},
		{"ppt/slides/slide10.xml", slide("Slide Ten")},
	})
	res, err := ExtractOOXML(raw, DefaultLimits())
	if err != nil {
		t.Fatalf("ExtractOOXML: %v", err)
	}
	if res.Subtype != SubtypePPTX {
		t.Errorf("subtype = %q, want pptx", res.Subtype)
	}
	// Numeric slide order (1,2,10), NOT lexical (1,10,2).
	if res.Text != "Slide One\nSlide Two\nSlide Ten" {
		t.Errorf("text = %q, want numeric slide order", res.Text)
	}
}

func TestExtractOOXML_XLSX_SharedStrings(t *testing.T) {
	raw := makeZip(t, []part{
		{"[Content_Types].xml", ctypes},
		{"xl/workbook.xml", `<workbook/>`},
		{"xl/sharedStrings.xml", `<sst><si><t>Cell A</t></si><si><t>Cell B</t></si></sst>`},
	})
	res, err := ExtractOOXML(raw, DefaultLimits())
	if err != nil {
		t.Fatalf("ExtractOOXML: %v", err)
	}
	if res.Subtype != SubtypeXLSX {
		t.Errorf("subtype = %q, want xlsx", res.Subtype)
	}
	if res.Text != "Cell A\nCell B" {
		t.Errorf("text = %q, want %q", res.Text, "Cell A\nCell B")
	}
}

func TestExtractOOXML_PredefinedEntities(t *testing.T) {
	// The five predefined XML entities are legitimate content and must decode.
	raw := makeZip(t, []part{
		{"[Content_Types].xml", ctypes},
		{"word/document.xml", docXML(`<w:p><w:t>A &amp; B &lt; C</w:t></w:p>`)},
	})
	res, err := ExtractOOXML(raw, DefaultLimits())
	if err != nil {
		t.Fatalf("ExtractOOXML: %v", err)
	}
	if res.Text != "A & B < C" {
		t.Errorf("text = %q, want %q", res.Text, "A & B < C")
	}
}

func TestExtractOOXML_EmptyText(t *testing.T) {
	// A document with no text runs yields empty text, NOT an error and NOT invented
	// content (the OCR-out-of-scope contract for the no-text-layer case).
	raw := makeZip(t, []part{
		{"[Content_Types].xml", ctypes},
		{"word/document.xml", docXML(`<w:p><w:r><w:drawing/></w:r></w:p>`)},
	})
	res, err := ExtractOOXML(raw, DefaultLimits())
	if err != nil {
		t.Fatalf("ExtractOOXML: %v", err)
	}
	if res.Text != "" {
		t.Errorf("text = %q, want empty", res.Text)
	}
}

func TestExtractOOXML_DuplicateSlidePartDeduped(t *testing.T) {
	// Two zip entries with the SAME slide name must NOT both be read (the first-wins
	// anti-spoofing invariant): a crafted deck cannot smuggle a second body that is
	// concatenated in and double-charged to the decompression budget.
	slide := func(s string) string {
		return `<p:sld xmlns:a="a"><a:p><a:t>` + s + `</a:t></a:p></p:sld>`
	}
	raw := makeZip(t, []part{
		{"[Content_Types].xml", ctypes},
		{"ppt/presentation.xml", `<p:presentation/>`},
		{"ppt/slides/slide1.xml", slide("FirstBody")},
		{"ppt/slides/slide1.xml", slide("SmuggledSecondBody")}, // duplicate name
	})
	res, err := ExtractOOXML(raw, DefaultLimits())
	if err != nil {
		t.Fatalf("ExtractOOXML: %v", err)
	}
	if !strings.Contains(res.Text, "FirstBody") {
		t.Errorf("first slide body missing: %q", res.Text)
	}
	if strings.Contains(res.Text, "SmuggledSecondBody") {
		t.Errorf("duplicate slide body was ingested (spoofing invariant violated): %q", res.Text)
	}
}

// --- hostile input battery ----------------------------------------------------

func TestExtractOOXML_RejectsEntityExpansion(t *testing.T) {
	// A DOCTYPE with a custom ENTITY and a reference to it: Go's encoding/xml does NOT
	// process the DTD, so &xxe; is an UNKNOWN entity → the parse errors and the doc is
	// rejected. The expanded value must NEVER appear in the output (no XXE / no
	// billion-laughs expansion is reachable).
	doc := `<?xml version="1.0"?><!DOCTYPE foo [<!ENTITY xxe "SECRET_EXPANDED">]>` +
		`<w:document xmlns:w="w"><w:body><w:p><w:t>&xxe;</w:t></w:p></w:body></w:document>`
	raw := makeZip(t, []part{
		{"[Content_Types].xml", ctypes},
		{"word/document.xml", doc},
	})
	res, err := ExtractOOXML(raw, DefaultLimits())
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
	if strings.Contains(res.Text, "SECRET_EXPANDED") {
		t.Fatalf("entity was expanded: %q", res.Text)
	}
}

func TestExtractOOXML_NotOOXML(t *testing.T) {
	// A valid zip that is not an OOXML package (no [Content_Types].xml).
	raw := makeZip(t, []part{{"random.txt", "just a zip"}})
	if _, err := ExtractOOXML(raw, DefaultLimits()); !errors.Is(err, ErrNotOOXML) {
		t.Fatalf("err = %v, want ErrNotOOXML", err)
	}
	// Content-types present but no recognized primary part.
	raw2 := makeZip(t, []part{{"[Content_Types].xml", ctypes}, {"misc/data.xml", "<x/>"}})
	if _, err := ExtractOOXML(raw2, DefaultLimits()); !errors.Is(err, ErrNotOOXML) {
		t.Fatalf("err = %v, want ErrNotOOXML for content-types-only", err)
	}
}

func TestExtractOOXML_MalformedZip(t *testing.T) {
	if _, err := ExtractOOXML([]byte("PK\x03\x04 not really a zip"), DefaultLimits()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
	if _, err := ExtractOOXML([]byte("totally random bytes"), DefaultLimits()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed for non-zip", err)
	}
}

func TestExtractOOXML_TooManyParts(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxParts = 8
	parts := make([]part, 0, lim.MaxParts+2)
	for i := 0; i < lim.MaxParts+2; i++ {
		parts = append(parts, part{name: "p" + string(rune('a'+i)) + ".xml", content: "<x/>"})
	}
	if _, err := ExtractOOXML(makeZip(t, parts), lim); !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed (part-count cap)", err)
	}
}

func TestExtractOOXML_ZipBombPartTooLarge(t *testing.T) {
	// A single part whose decompressed size exceeds the per-part cap is refused.
	lim := DefaultLimits()
	lim.MaxPartBytes = 1024
	lim.MaxTotalDecompressed = 4096
	big := docXML(`<w:p><w:t>` + strings.Repeat("A", 4000) + `</w:t></w:p>`)
	raw := makeZip(t, []part{
		{"[Content_Types].xml", ctypes},
		{"word/document.xml", big},
	})
	if _, err := ExtractOOXML(raw, lim); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge (per-part cap)", err)
	}
}

func TestExtractOOXML_InputTooLarge(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxInputBytes = 32
	raw := makeZip(t, []part{{"[Content_Types].xml", ctypes}, {"word/document.xml", docXML(`<w:p><w:t>x</w:t></w:p>`)}})
	if _, err := ExtractOOXML(raw, lim); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge (input cap)", err)
	}
}

func TestExtractOOXML_DeepNestingRejected(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxDepth = 50
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("<w:x>")
	}
	sb.WriteString("<w:t>deep</w:t>")
	for i := 0; i < 200; i++ {
		sb.WriteString("</w:x>")
	}
	raw := makeZip(t, []part{
		{"[Content_Types].xml", ctypes},
		{"word/document.xml", docXML(sb.String())},
	})
	if _, err := ExtractOOXML(raw, lim); !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed (depth cap)", err)
	}
}

func TestExtractOOXML_OutputCap(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxOutputBytes = 5
	raw := makeZip(t, []part{
		{"[Content_Types].xml", ctypes},
		{"word/document.xml", docXML(`<w:p><w:t>` + strings.Repeat("Z", 100) + `</w:t></w:p>`)},
	})
	res, err := ExtractOOXML(raw, lim)
	if err != nil {
		t.Fatalf("ExtractOOXML: %v", err)
	}
	if len(res.Text) > lim.MaxOutputBytes {
		t.Fatalf("output %d bytes exceeds cap %d", len(res.Text), lim.MaxOutputBytes)
	}
}

func TestExtractOOXML_Deterministic(t *testing.T) {
	raw := makeZip(t, []part{
		{"[Content_Types].xml", ctypes},
		{"ppt/presentation.xml", `<p:presentation/>`},
		{"ppt/slides/slide1.xml", `<p:sld xmlns:a="a"><a:p><a:t>One</a:t></a:p></p:sld>`},
		{"ppt/slides/slide2.xml", `<p:sld xmlns:a="a"><a:p><a:t>Two</a:t></a:p></p:sld>`},
	})
	first, err := ExtractOOXML(raw, DefaultLimits())
	if err != nil {
		t.Fatalf("ExtractOOXML: %v", err)
	}
	for i := 0; i < 5; i++ {
		got, err := ExtractOOXML(raw, DefaultLimits())
		if err != nil || got.Text != first.Text {
			t.Fatalf("run %d: text=%q err=%v, want stable %q", i, got.Text, err, first.Text)
		}
	}
}

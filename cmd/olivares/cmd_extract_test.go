// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"archive/zip"
	"bytes"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
)

func buildDOCX(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, content string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	add("[Content_Types].xml", `<Types/>`)
	add("word/document.xml", `<?xml version="1.0"?><w:document xmlns:w="w"><w:body><w:p><w:t>`+body+`</w:t></w:p></w:body></w:document>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractOnce_DOCXSuccess(t *testing.T) {
	var out bytes.Buffer
	code, err := extractOnce(bytes.NewReader(buildDOCX(t, "Hello World")), &out, string(contentsource.RichDocOOXML))
	if err != nil || code != 0 {
		t.Fatalf("extractOnce = (%d, %v), want (0, nil)", code, err)
	}
	if got := out.String(); got != "docx\nHello World" {
		t.Errorf("output = %q, want %q", got, "docx\nHello World")
	}
}

func TestExtractOnce_UnsupportedKind(t *testing.T) {
	var out bytes.Buffer
	code, err := extractOnce(bytes.NewReader([]byte("%PDF-1.7")), &out, "pdf")
	if err != nil || code != extractSkipExitCode {
		t.Fatalf("extractOnce = (%d, %v), want (%d, nil)", code, err, extractSkipExitCode)
	}
	if out.Len() != 0 {
		t.Errorf("unsupported kind wrote output: %q", out.String())
	}
}

func TestExtractOnce_NotOOXMLSkips(t *testing.T) {
	// A valid zip that is not OOXML → classified skip (exit code), never an error.
	var z bytes.Buffer
	zw := zip.NewWriter(&z)
	w, _ := zw.Create("random.txt")
	_, _ = w.Write([]byte("hi"))
	_ = zw.Close()

	var out bytes.Buffer
	code, err := extractOnce(bytes.NewReader(z.Bytes()), &out, string(contentsource.RichDocOOXML))
	if err != nil || code != extractSkipExitCode {
		t.Fatalf("extractOnce(not-ooxml) = (%d, %v), want (%d, nil)", code, err, extractSkipExitCode)
	}
}

func TestExtractOnce_MalformedSkips(t *testing.T) {
	var out bytes.Buffer
	code, err := extractOnce(bytes.NewReader([]byte("PK\x03\x04 garbage")), &out, string(contentsource.RichDocOOXML))
	if err != nil || code != extractSkipExitCode {
		t.Fatalf("extractOnce(malformed) = (%d, %v), want (%d, nil)", code, err, extractSkipExitCode)
	}
}

func TestSplitExtractOutput(t *testing.T) {
	cases := []struct {
		in            string
		subtype, text string
	}{
		{"docx\nhello world", "docx", "hello world"},
		{"pptx\nline1\nline2", "pptx", "line1\nline2"},
		{"xlsx\n", "xlsx", ""},
		{"no-newline", "", ""},
		{"", "", ""},
	}
	for _, tc := range cases {
		st, txt := splitExtractOutput([]byte(tc.in))
		if st != tc.subtype || txt != tc.text {
			t.Errorf("splitExtractOutput(%q) = (%q, %q), want (%q, %q)", tc.in, st, txt, tc.subtype, tc.text)
		}
	}
}

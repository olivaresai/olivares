// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package documents

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/runtime/confine"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == confine.HelperArg {
		os.Exit(confine.RunHelper(os.Args[2:]))
	}
	os.Exit(m.Run())
}
func TestDocumentConversionIsOfflineAndPreservesOriginal(t *testing.T) {
	if python := os.Getenv("OLIVARES_TEST_MARKITDOWN_PYTHON"); python != "" {
		converterPython = python
	} else if _, err := os.Stat(Python); errors.Is(err, os.ErrNotExist) {
		t.Skip("requires the packaged MarkItDown environment or OLIVARES_TEST_MARKITDOWN_PYTHON")
	}
	originalHelper := helperCode
	defer func() { helperCode = originalHelper }()
	dir := t.TempDir()
	path := filepath.Join(dir, "report.csv")
	original := "item,total\nRevenue,42\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	got, err := Convert(t.Context(), root, "report.csv")
	if err != nil {
		t.Fatal(err)
	}
	md, err := os.ReadFile(filepath.Join(dir, got))
	if err != nil || !strings.Contains(string(md), "42") {
		t.Fatalf("Markdown %q: %v", md, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatal("original changed")
	}
	if _, err := Convert(t.Context(), root, "report.csv"); err == nil {
		t.Fatal("overwrote a derived file")
	}
	for _, code := range []string{
		"import socket; socket.socket(); print('escaped')",
		"print(open(" + `"` + filepath.Join(dir, "secret") + `"` + ").read())",
		"print('x' * (32*1024*1024+1))",
	} {
		if err := os.WriteFile(filepath.Join(dir, "secret"), []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
		helperCode = code
		if _, err := Convert(t.Context(), root, "report.csv"); err == nil || !strings.Contains(err.Error(), "conversion refused") {
			t.Fatalf("unsafe converter was not refused: %v", err)
		}
	}
}

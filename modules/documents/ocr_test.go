// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package documents

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/runtime/confine"
	conversion "github.com/olivaresai/olivares/core/runtime/documents"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == confine.HelperArg {
		os.Exit(confine.RunHelper(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func TestStoppedOCRModuleSendsNoRequests(t *testing.T) {
	if _, err := os.Stat(conversion.Python); os.IsNotExist(err) {
		t.Skip("requires the packaged document converter")
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("stopped OCR module sent a request") }))
	defer server.Close()
	ocr := NewOCR(conversion.OCRConfig{Endpoint: server.URL, Model: "Unlimited-OCR-2@sha256:" + strings.Repeat("a", 64)})
	if err := ocr.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ocr.Config().Endpoint == "" {
		t.Fatal("running module did not expose configured endpoint")
	}
	docs := New(ocr.Config)
	if err := docs.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := ocr.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ocr.Config() != (conversion.OCRConfig{}) || docs.ImageOCR() {
		t.Fatal("stopped module retained its endpoint")
	}
	var input bytes.Buffer
	if err := png.Encode(&input, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	got, err := docs.Markdown(t.Context(), "scan.png", input.Bytes())
	if err != nil || !bytes.Contains(got, []byte(conversion.OCRDisabled)) {
		t.Fatalf("stopped OCR conversion: %q %v", got, err)
	}
}

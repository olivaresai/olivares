// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package documents

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

func TestScannedDocumentOCR(t *testing.T) {
	oldPython, oldCode := converterPython, helperCode
	defer func() { converterPython, helperCode = oldPython, oldCode }()
	converterPython = "/usr/bin/python3"
	helperCode = `print('')`
	md, err := Markdown(t.Context(), "scan.pdf", []byte("scanned PDF"))
	if err == nil || len(md) != 0 || !strings.Contains(err.Error(), OCRDisabled) {
		t.Fatalf("scanned document must fail with OCR-off notice: %q, %v", md, err)
	} // The same three-page converter result is inert when off and sent in order when on.
	helperCode = `import json, sys
if sys.argv[2] == 'off':
    sys.exit(0)
json.dump([{"text":"", "image":"data:image/jpeg;base64,eA=="}]*3, __import__('sys').stdout)`
	calls := 0
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Content []map[string]any `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "Unlimited-OCR-2@sha256:"+strings.Repeat("a", 64) || r.URL.Path != "/v1/chat/completions" {
			t.Error("invalid OCR request")
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"receipt page %d: Total 42"}}]}`, calls)
	}))
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	// Use this host's non-loopback address: Go bypasses environment proxies for
	// loopback even with ProxyFromEnvironment, so loopback cannot prove this rule.
	endpointHost := ""
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err == nil && ip.To4() != nil && !ip.IsLoopback() {
			endpointHost = ip.String()
			break
		}
	}
	if endpointHost == "" {
		t.Fatal("non-loopback host address required for proxy regression")
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + net.JoinHostPort(endpointHost, port)
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("OCR used an environment proxy") }))
	defer proxy.Close()
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(key, proxy.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	cfg := OCRConfig{Endpoint: endpoint + "/v1", Model: "Unlimited-OCR-2@sha256:" + strings.Repeat("a", 64)}
	md, err = Markdown(t.Context(), "scan.pdf", []byte("scan"))
	if err == nil || len(md) != 0 || !strings.Contains(err.Error(), OCRDisabled) || calls != 0 {
		t.Fatalf("off: %q %v calls=%d", md, err, calls)
	}
	md, err = MarkdownWithOCR(t.Context(), "scan.pdf", []byte("scan"), cfg)
	if err != nil || !strings.Contains(string(md), "receipt page 3: Total 42") || calls != 3 {
		t.Fatalf("on: %q %v calls=%d", md, err, calls)
	}
	for _, endpoint := range []string{"http://192.0.2.1/v1", "http://example.com/v1", "http://127.0.0.1.example.com/v1", "http://localhost@192.0.2.1/v1", "http://[2001:db8::1]/v1", "http://[::ffff:192.0.2.1]/v1"} {
		bad := cfg
		bad.Endpoint = endpoint
		if err := bad.Validate(); err == nil {
			t.Fatalf("accepted remote endpoint %s", endpoint)
		}
	}
	for _, endpoint := range []string{"http://[::1]/v1", "http://[::ffff:127.0.0.1]/v1", "http://localhost/v1"} {
		local := cfg
		local.Endpoint = endpoint
		if err := local.Validate(); err != nil {
			t.Fatalf("refused local endpoint %s: %v", endpoint, err)
		}
	}
	remote := cfg
	if err := json.Unmarshal([]byte(`{"endpoint":"https://example.com/v1","allow_remote":true}`), &remote); err != nil {
		t.Fatal(err)
	}
	if err := remote.Validate(); err == nil {
		t.Fatal("accepted remote endpoint through legacy allow_remote setting")
	}
	bad := cfg
	bad.Model = "Unlimited-OCR-2"
	if err := bad.Validate(); err == nil {
		t.Fatal("accepted unpinned model")
	}
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://192.0.2.1/", http.StatusTemporaryRedirect)
	})
	if _, err := cfg.recognize(t.Context(), "data:image/jpeg;base64,eA=="); err == nil {
		t.Fatal("accepted redirect")
	}

}

func TestPDFMarkItDownCompatibility(t *testing.T) {
	oldPython := converterPython
	defer func() { converterPython = oldPython }()
	if python := os.Getenv("OLIVARES_TEST_MARKITDOWN_PYTHON"); python != "" {
		converterPython = python
	}
	if _, err := os.Stat(converterPython); err != nil {
		if os.Getenv("OLIVARES_TEST_MARKITDOWN_PYTHON") != "" {
			t.Fatal("CI requires the hash-locked MarkItDown environment")
		}
		t.Skip("requires the packaged MarkItDown environment or OLIVARES_TEST_MARKITDOWN_PYTHON")
	}
	// A valid PDF with 101 text pages catches the newly introduced page cap as well as formatting changes.
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	object := func(body string) {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", len(offsets)-1, body)
	}
	object("<< /Type /Catalog /Pages 2 0 R >>")
	var kids strings.Builder
	for i := 0; i < 101; i++ {
		fmt.Fprintf(&kids, "%d 0 R ", 5+i)
	}
	object(fmt.Sprintf("<< /Type /Pages /Count 101 /Kids [%s] >>", kids.String()))
	object("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	text := "BT /F1 12 Tf 50 700 Td (Revenue 42) Tj ET"
	object(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(text), text))
	for i := 0; i < 101; i++ {
		object("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents 4 0 R >>")
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	legacy := exec.Command(converterPython, "-I", "-c", "from markitdown import MarkItDown; import sys; sys.stdout.write(MarkItDown(enable_plugins=False).convert_stream(sys.stdin.buffer, file_extension='.pdf').text_content)")
	legacy.Stdin = bytes.NewReader(pdf.Bytes())
	want, err := legacy.Output()
	if err != nil || !bytes.Contains(want, []byte("Revenue 42")) {
		t.Fatalf("legacy converter: %q %v", want, err)
	}
	got, err := Markdown(t.Context(), "report.pdf", pdf.Bytes())
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("OCR off changed MarkItDown output: got %d bytes, want %d: %v", len(got), len(want), err)
	}
	// Exercise the real confined PDF renderer and its locked PDF/PIL imports.
	scan := exec.Command(converterPython, "-I", "-c", `from PIL import Image, ImageDraw
import io, sys
pages = []
for i in range(3):
    image = Image.new('RGB', (320, 240), 'white')
    ImageDraw.Draw(image).text((20, 20), 'Receipt page %d: Total 42' % (i+1), fill='black')
    pages.append(image)
out = io.BytesIO()
pages[0].save(out, format='PDF', save_all=True, append_images=pages[1:])
sys.stdout.buffer.write(out.getvalue())`)
	data, err := scan.Output()
	if err != nil {
		t.Fatalf("scan fixture: %v (%s)", err, err.(*exec.ExitError).Stderr)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content []struct {
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		if len(request.Messages) != 1 || len(request.Messages[0].Content) != 2 {
			t.Error("missing page image")
			http.Error(w, "bad request", 400)
			return
		}
		image, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(request.Messages[0].Content[0].ImageURL.URL, "data:image/jpeg;base64,"))
		if err != nil {
			t.Error(err)
		}
		if _, err := jpeg.Decode(bytes.NewReader(image)); err != nil {
			t.Error(err)
		}
		calls++
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"Receipt page %d: Total 42"}}]}`, calls)
	}))
	defer server.Close()
	if md, err := Markdown(t.Context(), "scan.pdf", data); err == nil || len(md) != 0 || !strings.Contains(err.Error(), OCRDisabled) || calls != 0 {
		t.Fatalf("OCR off: %q %v calls=%d", md, err, calls)
	}
	cfg := OCRConfig{Endpoint: server.URL, Model: "Unlimited-OCR-2@sha256:" + strings.Repeat("a", 64)}
	md, err := MarkdownWithOCR(t.Context(), "scan.pdf", data, cfg)
	if err != nil || calls != 3 || !strings.Contains(string(md), "## Page 3\n\nReceipt page 3: Total 42") {
		t.Fatalf("real helper: %q %v calls=%d", md, err, calls)
	}
	t.Log("real helper: three rendered JPEG pages sent to local protocol stand-in; page 3 text returned")

}

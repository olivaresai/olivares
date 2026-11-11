// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package documents runs the packaged MarkItDown helper with no access to user
// folders or the network. The input is a bounded pipe; only the parent writes a
// derived Markdown file under the caller's already authorized filesystem root.
package documents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/core/runtime/confine"
)

const Python = "/usr/lib/olivares/markitdown/bin/python3"
const MaxBytes = 32 << 20

// ErrBusy lets upload callers reuse their existing Retry-After backoff.
var ErrBusy = errors.New("document converters are busy; try again in a moment")
var conversionSlots = make(chan struct{}, 2)
var converterPython = Python
var helperCode = `import base64, io, json, resource, sys
resource.setrlimit(resource.RLIMIT_AS, (768*1024*1024, 768*1024*1024))
resource.setrlimit(resource.RLIMIT_CPU, (25, 25))
from markitdown import MarkItDown
raw = sys.stdin.buffer.read()
ext = sys.argv[1]
ocr = sys.argv[2] == 'on'
pages = []
if ext == '.pdf' and ocr:
    import pdfplumber
    import pypdfium2
    with pdfplumber.open(io.BytesIO(raw)) as pdf:
        renderer = None
        for i, page in enumerate(pdf.pages):
            text = page.extract_text() or ''
            image = ''
            if renderer is None:
                renderer = pypdfium2.PdfDocument(raw)
            if text.strip():
                with pypdfium2.PdfDocument.new() as single:
                    single.import_pages(renderer, [i])
                    out = io.BytesIO()
                    single.save(out)
                    out.seek(0)
                    text = MarkItDown(enable_plugins=False).convert_stream(out, file_extension=ext).text_content
            else:
                view = renderer[i]
                width, height = view.get_size()
                scale = min(2, 2048 / max(width, height))
                bitmap = view.render(scale=scale)
                out = io.BytesIO()
                bitmap.to_pil().convert('RGB').save(out, format='JPEG')
                image = 'data:image/jpeg;base64,' + base64.b64encode(out.getvalue()).decode()
                bitmap.close()
                view.close()
            pages.append({'text': text, 'image': image})
        if renderer is not None:
            renderer.close()
elif ext in ('.png', '.jpg', '.jpeg', '.webp', '.gif'):
    from PIL import Image
    with Image.open(io.BytesIO(raw)) as image:
        image.load()
        encoded = ''
        if ocr:
            image.thumbnail((2048, 2048))
            out = io.BytesIO()
            image.convert('RGB').save(out, format='JPEG')
            encoded = 'data:image/jpeg;base64,' + base64.b64encode(out.getvalue()).decode()
        pages.append({'text': '', 'image': encoded})
else:
    text = MarkItDown(enable_plugins=False).convert_stream(io.BytesIO(raw), file_extension=ext).text_content
    if not text and ext != '.pdf':
        raise ValueError('MarkItDown produced no Markdown')
    sys.stdout.write(text)
    sys.exit(0)
json.dump(pages, sys.stdout)
`

// Supports reports formats converted locally by the packaged dependency set.
func Supports(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".pdf", ".docx", ".pptx", ".xlsx", ".html", ".htm", ".csv", ".json", ".xml", ".epub", ".zip":
		return true
	}
	return false
}

// Markdown converts bytes without letting the helper open the original pathname.
func Markdown(ctx context.Context, name string, data []byte) ([]byte, error) {
	return MarkdownWithOCR(ctx, name, data, OCRConfig{})
}

// MarkdownWithOCR uses the same confined converter with optional OCR for textless pages.
func MarkdownWithOCR(ctx context.Context, name string, data []byte, ocr OCRConfig) ([]byte, error) {
	if err := ocr.Validate(); err != nil {
		return nil, err
	}
	select {
	case conversionSlots <- struct{}{}:
		defer func() { <-conversionSlots }()
	default:
		return nil, ErrBusy
	}
	ext := strings.ToLower(filepath.Ext(name))
	if !Supports(name) {
		return nil, fmt.Errorf("document format %q is not supported", ext)
	}
	if len(data) == 0 || len(data) > MaxBytes {
		return nil, fmt.Errorf("document %s must contain between 1 and %d bytes", ext, MaxBytes)
	}
	if _, err := os.Stat(converterPython); err != nil {
		return nil, fmt.Errorf("MarkItDown is unavailable for %s; install the packaged document converter: %w", ext, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	mode := "off"
	if ocr.Endpoint != "" {
		mode = "on"
	}
	helperCtx, stopHelper := context.WithTimeout(ctx, 30*time.Second)
	defer stopHelper()
	cmd := exec.CommandContext(helperCtx, converterPython, "-I", "-c", helperCode, ext, mode)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "OPENBLAS_NUM_THREADS=1", "MALLOC_ARENA_MAX=2"}
	cmd.Dir = "/"
	cmd.Stdin = bytes.NewReader(data)
	var stdout, stderr boundedBuffer
	stdout.limit = MaxBytes
	stderr.limit = 4096
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	policy := confine.Policy{Offline: true, ReadOnly: []string{"/usr", "/lib", filepath.Dir(filepath.Dir(converterPython))}, Limits: &confine.Limits{FileSize: MaxBytes, Nice: 10}}
	// Python's standard MIME database is runtime data, not a user directory.
	if info, err := os.Stat("/etc/mime.types"); err == nil && info.Mode().IsRegular() {
		policy.ReadOnly = append(policy.ReadOnly, "/etc/mime.types")
	}
	if _, err := os.Stat("/lib64"); err == nil {
		policy.ReadOnly = append(policy.ReadOnly, "/lib64")
	}
	state, err := confine.Wrap(cmd, policy)
	if err != nil {
		return nil, fmt.Errorf("%s conversion refused by confinement: %w", ext, err)
	}
	if state.Mode != confine.ModeLandlock || state.ABI < 3 {
		return nil, fmt.Errorf("%s conversion refused: Landlock ABI 3 or later is required (%s)", ext, state.Reason)
	}
	if err := cmd.Run(); err != nil {
		if helperCtx.Err() != nil {
			return nil, fmt.Errorf("%s conversion refused: the time bound was exceeded: %w", ext, helperCtx.Err())
		}
		return nil, fmt.Errorf("%s conversion refused (offline, filesystem or format restriction): %w: %s", ext, err, strings.TrimSpace(string(stderr.data)))
	}
	// Keep the original raw output protocol for MarkItDown conversions, so JSON
	// encoding cannot reduce their existing 32 MiB output ceiling.
	if !IsImage(name) && (ext != ".pdf" || ocr.Endpoint == "") {
		if ext == ".pdf" && strings.TrimSpace(string(stdout.data)) == "" {
			return nil, fmt.Errorf("MarkItDown produced no Markdown: %s", OCRDisabled)
		}
		if len(stdout.data) == 0 {
			return nil, errors.New("MarkItDown produced no Markdown")
		}
		return stdout.data, nil
	}
	var pages []documentPage
	if err := json.Unmarshal(stdout.data, &pages); err != nil || len(pages) == 0 {
		return nil, errors.New("document converter produced invalid pages")
	}
	var md boundedBuffer
	md.limit = MaxBytes
	for i, page := range pages {
		text := page.Text
		if strings.TrimSpace(text) == "" {
			if ocr.Endpoint == "" {
				text = OCRDisabled
			} else {
				if !strings.HasPrefix(page.Image, "data:image/jpeg;base64,") {
					return nil, errors.New("OCR page image is unavailable")
				}
				text, err = ocr.recognize(ctx, page.Image)
				if err != nil {
					return nil, fmt.Errorf("OCR page %d: %w", i+1, err)
				}
			}
		}
		if ext == ".pdf" {
			text = fmt.Sprintf("## Page %d\n\n%s", i+1, text)
		}
		if _, err := md.Write([]byte(text + "\n\n")); err != nil {
			return nil, err
		}
	}
	return md.data, nil
}

type boundedBuffer struct {
	data  []byte
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-len(b.data) {
		return 0, errors.New("document converter output exceeds its limit")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

// Convert preserves the original and refuses to overwrite an existing derivative.
// root is the caller's authorized folder, not a path supplied by a remote client.
func Convert(ctx context.Context, root *os.Root, name string, options ...OCRConfig) (string, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("open document: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxBytes {
		return "", errors.New("document must be a regular file of at most 32 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return "", err
	}
	var ocr OCRConfig
	if len(options) > 0 {
		ocr = options[0]
	}
	md, err := MarkdownWithOCR(ctx, name, data, ocr)
	if err != nil {
		return "", err
	}
	out := name + ".md"
	dest, err := root.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("create Markdown beside original: %w", err)
	}
	_, werr := dest.Write(md)
	cerr := dest.Close()
	if err := errors.Join(werr, cerr); err != nil {
		return "", errors.Join(err, root.Remove(out))
	}
	return out, nil
}

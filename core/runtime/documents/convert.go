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
var helperCode = `import resource, sys
resource.setrlimit(resource.RLIMIT_AS, (768*1024*1024, 768*1024*1024))
resource.setrlimit(resource.RLIMIT_CPU, (25, 25))
from markitdown import MarkItDown
result = MarkItDown(enable_plugins=False).convert_stream(sys.stdin.buffer, file_extension=sys.argv[1])
sys.stdout.write(result.text_content)
`

// Supports reports formats converted locally by the packaged dependency set.
func Supports(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf", ".docx", ".pptx", ".xlsx", ".html", ".htm", ".csv", ".json", ".xml", ".epub", ".zip":
		return true
	}
	return false
}

// Markdown converts bytes without letting the helper open the original pathname.
func Markdown(ctx context.Context, name string, data []byte) ([]byte, error) {
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
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, converterPython, "-I", "-c", helperCode, ext)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "OPENBLAS_NUM_THREADS=1"}
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
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s conversion refused: the time bound was exceeded: %w", ext, ctx.Err())
		}
		return nil, fmt.Errorf("%s conversion refused (offline, filesystem or format restriction): %w: %s", ext, err, strings.TrimSpace(string(stderr.data)))
	}
	if len(stdout.data) == 0 {
		return nil, fmt.Errorf("%s conversion refused: MarkItDown produced no Markdown", ext)
	}
	return stdout.data, nil
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
func Convert(ctx context.Context, root *os.Root, name string) (string, error) {
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
	md, err := Markdown(ctx, name, data)
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

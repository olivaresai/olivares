//go:build contract && linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package protocolcontract

import (
	"bytes"
	"io"
	"testing"
)

func TestContractCaptureKeepsPrefixAndDrainsCopy(t *testing.T) {
	prefix := bytes.Repeat([]byte("x"), 4<<20)
	payload := append(append([]byte(nil), prefix...), bytes.Repeat([]byte("y"), 1<<20)...)
	for _, writerTo := range []bool{false, true} {
		name := "reader-only"
		if writerTo {
			name = "writer-to"
		}
		t.Run(name, func(t *testing.T) {
			var capture capBuffer
			var src io.Reader = bytes.NewReader(payload)
			if !writerTo {
				// Match the stderr pipe's interface: Reader, without WriterTo.
				src = struct{ io.Reader }{src}
			}
			n, err := io.Copy(&capture, src)
			if err != nil || n != int64(len(payload)) {
				t.Fatalf("source not fully drained: bytes=%d error=%v", n, err)
			}
			if !bytes.Equal(capture.Bytes(), prefix) {
				t.Fatalf("capture did not retain exactly the first four MiB: bytes=%d", capture.Len())
			}
		})
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// chunked returns r in reads of at most n bytes, so a marker is cut wherever n falls.
type chunked struct {
	r io.Reader
	n int
}

func (c chunked) Read(p []byte) (int, error) { return c.r.Read(p[:min(len(p), c.n)]) }

func TestImageStripReaderDropsOnlyTheBytesOfInlineImages(t *testing.T) {
	big := strings.Repeat("QUJDREVGR0g/+", 5000) + "=="
	for _, test := range []struct{ name, in, want string }{
		{"an image in a json string", `{"type":"image","url":"data:image/png;base64,` + big + `"}` + "\n",
			`{"type":"image","url":"data:image/png;base64,"}` + "\n"},
		{"two images and the text between", `["data:image/webp;base64,AAAA","a b","data:image/svg+xml;base64,PHN2Zz4="]`,
			`["data:image/webp;base64,","a b","data:image/svg+xml;base64,"]`},
		{"lines stay lines", "data:image/gif;base64,AAAA\nnext\n", "data:image/gif;base64,\nnext\n"},
		{"a payload that ends the stream", `x data:image/png;base64,AAAA`, `x data:image/png;base64,`},
		{"not an image type", `"data:text/plain;base64,QUJD"`, `"data:text/plain;base64,QUJD"`},
		{"no base64 marker", `"data:image/png,AAAA"`, `"data:image/png,AAAA"`},
		{"marker without its comma", `"data:image/png;base64AAAA"`, `"data:image/png;base64AAAA"`},
		{"head cut by the end of the stream", `ends in data:image/pn`, `ends in data:image/pn`},
		{"d-words that are not the head", `dd data:imagedata:image/x ddata`, `dd data:imagedata:image/x ddata`},
		{"a subtype that is not a name", `"data:image/` + strings.Repeat("a", 40) + `;base64,AAAA"`, `"data:image/` + strings.Repeat("a", 40) + `;base64,AAAA"`},
		{"nothing to strip", `{"id":1,"result":{"text":"hello"}}` + "\n", `{"id":1,"result":{"text":"hello"}}` + "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Every way a read can cut the stream gives the same answer.
			for _, n := range []int{1, 2, 3, 5, 7, 11, 64, 4096, 1 << 20} {
				got, err := io.ReadAll(newImageStripReader(chunked{strings.NewReader(test.in), n}))
				if err != nil || string(got) != test.want {
					t.Fatalf("chunk %d: got %q (%v); want %q", n, got, err, test.want)
				}
			}
		})
	}
}

func TestImageStripReaderPassesTheSourceError(t *testing.T) {
	r := newImageStripReader(iotest.TimeoutReader(strings.NewReader("data:image/png;base64,AAAA\nrest")))
	got, err := io.ReadAll(r)
	if err != iotest.ErrTimeout || string(got) != "data:image/png;base64,\nrest" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// Closing the wrapped output closes the pipe under it, which is what ends a pump that
// is blocked in a read.
func TestImageStripReadCloserClosesTheSource(t *testing.T) {
	r, w := io.Pipe()
	out := stripImagesFrom(r, true)
	done := make(chan error, 1)
	go func() {
		_, err := out.Read(make([]byte, 8))
		done <- err
	}()
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != io.ErrClosedPipe {
		t.Fatalf("read after close = %v; want %v", err, io.ErrClosedPipe)
	}
	_ = w.Close()
	if plain := stripImagesFrom(r, false); plain != io.ReadCloser(r) {
		t.Fatal("a launch that does not strip must read the source itself")
	}
}

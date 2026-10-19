// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "io"

// imageStripReader passes a JSON-lines stream through and drops the bytes of every
// inline image: after `data:image/<type>;base64,` it skips the base64 payload and keeps
// everything else, so a line that listed a 1.2 MB screenshot is a few hundred bytes.
//
// A tool lists every image a person attached in the answers that give back its stored
// history (codex-cli 0.162.1: thread/read, thread/turns/list), and the pump ends the
// process for a line over its limit. The stored-conversation reader shows the text of
// a conversation, not its pictures (those come from the attachment route), so it reads
// the stream through this. It works on bytes as they arrive, holds at most a marker's
// worth, and does not depend on where a read ends.
type imageStripReader struct {
	src   io.Reader
	in    [32 << 10]byte
	out   []byte // processed bytes; out[off:] are not yet returned
	off   int
	held  []byte // a marker's bytes, not yet known to be one
	state int
	at    int // bytes of the literal being matched
	err   error
}

const (
	stripPlain = iota // outside a marker
	stripHead         // matching "data:image/"
	stripType         // the subtype, up to ';'
	stripMark         // matching ";base64,"
	stripSkip         // inside the payload
)

const (
	stripHeadLiteral = "data:image/"
	stripMarkLiteral = ";base64,"
	// stripMaxType bounds a subtype name; a longer one is not a media type.
	stripMaxType = 32
)

func newImageStripReader(src io.Reader) *imageStripReader { return &imageStripReader{src: src} }

// stripImagesFrom is rc as the launch's output: read through the strip when the launch
// asked for it, and closing rc itself either way, so the owner that retains the read
// end to bound a teardown still closes the pipe (or the terminal).
func stripImagesFrom(rc io.ReadCloser, strip bool) io.ReadCloser {
	if !strip {
		return rc
	}
	return struct {
		io.Reader
		io.Closer
	}{newImageStripReader(rc), rc}
}

func (r *imageStripReader) Read(p []byte) (int, error) {
	for r.off == len(r.out) {
		if r.err != nil {
			return 0, r.err
		}
		r.out, r.off = r.out[:0], 0
		n, err := r.src.Read(r.in[:])
		for _, b := range r.in[:n] {
			r.feed(b)
		}
		if err != nil {
			r.err = err
			// A marker cut short by the end of the stream was not one.
			r.out = append(r.out, r.held...)
			r.held = r.held[:0]
		}
	}
	n := copy(p, r.out[r.off:])
	r.off += n
	return n, nil
}

func (r *imageStripReader) feed(b byte) {
	switch r.state {
	case stripPlain:
		if b == stripHeadLiteral[0] {
			r.held = append(r.held[:0], b)
			r.state, r.at = stripHead, 1
			return
		}
		r.out = append(r.out, b)
	case stripHead:
		if b != stripHeadLiteral[r.at] {
			r.restart(b)
			return
		}
		r.held = append(r.held, b)
		if r.at++; r.at == len(stripHeadLiteral) {
			r.state = stripType
		}
	case stripType:
		switch {
		case b == stripMarkLiteral[0]:
			r.held = append(r.held, b)
			r.state, r.at = stripMark, 1
		case isMediaTypeByte(b) && len(r.held) < len(stripHeadLiteral)+stripMaxType:
			r.held = append(r.held, b)
		default:
			r.restart(b)
		}
	case stripMark:
		if b != stripMarkLiteral[r.at] {
			r.restart(b)
			return
		}
		r.held = append(r.held, b)
		if r.at++; r.at == len(stripMarkLiteral) {
			// The head stays: it says an image was here.
			r.out = append(r.out, r.held...)
			r.held = r.held[:0]
			r.state = stripSkip
		}
	case stripSkip:
		if isBase64Byte(b) {
			return
		}
		r.state = stripPlain
		r.feed(b)
	}
}

// restart passes on what was held (it was not a marker) and takes b afresh. A marker
// that began inside what was held is not looked for: a pathological
// `data:image/data:image/...` stays whole and fails closed at the line limit.
func (r *imageStripReader) restart(b byte) {
	r.out = append(r.out, r.held...)
	r.held = r.held[:0]
	r.state = stripPlain
	r.feed(b)
}

func isMediaTypeByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '.' || b == '+' || b == '-'
}

func isBase64Byte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '+' || b == '/' || b == '='
}

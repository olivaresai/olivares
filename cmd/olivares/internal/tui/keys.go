// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"bufio"
	"io"
	"unicode/utf8"
)

// The key map of CLI-CONCEPT §4, decoded once here so every screen agrees on
// what an arrow key, a Ctrl chord or a lone Esc means.

type keyKind int

const (
	keyNone keyKind = iota
	keyUp
	keyDown
	keyLeft
	keyRight
	keyEnter
	keyAltEnter
	keyEsc
	keyTab
	keyShiftTab
	keyBackspace
	keyCtrlC
	keyCtrlD
	keyCtrlO
	keyCtrlT
	keyRune
	// keyPasteStart and keyPasteEnd bracket pasted text (CSI 200~ and 201~): a
	// newline inside a paste is text, never a send.
	keyPasteStart
	keyPasteEnd
)

type key struct {
	kind keyKind
	rune rune
}

// escapeWindowMS is how long a lone ESC waits for the rest of a sequence. A
// terminal sends an arrow key as one burst; 30 ms is shorter than a person can
// press Esc twice and long enough for the bytes to arrive.
const escapeWindowMS = 30

// readKey decodes one key from the terminal. fd is the input descriptor, used
// only to ask whether more bytes follow an ESC; r supplies the bytes.
func readKey(r *bufio.Reader, fd int) (key, error) {
	b, err := r.ReadByte()
	if err != nil {
		return key{}, err
	}
	switch b {
	case 0x03:
		return key{kind: keyCtrlC}, nil
	case 0x04:
		return key{kind: keyCtrlD}, nil
	case 0x08, 0x7f:
		return key{kind: keyBackspace}, nil
	case 0x09:
		return key{kind: keyTab}, nil
	case 0x0f:
		return key{kind: keyCtrlO}, nil
	case 0x14:
		return key{kind: keyCtrlT}, nil
	case '\r', '\n':
		return key{kind: keyEnter}, nil
	case 0x1b:
		return readEscape(r, fd)
	}
	if b < utf8.RuneSelf {
		return key{kind: keyRune, rune: rune(b)}, nil
	}
	if err := r.UnreadByte(); err != nil {
		return key{}, err
	}
	ru, _, err := r.ReadRune()
	if err != nil {
		return key{}, err
	}
	return key{kind: keyRune, rune: ru}, nil
}

func readEscape(r *bufio.Reader, fd int) (key, error) {
	// A sequence already in the buffer is complete; only a terminal needs the
	// wait, and only for the first byte after ESC.
	if r.Buffered() == 0 && !inputPending(fd, escapeWindowMS) {
		return key{kind: keyEsc}, nil
	}
	b, err := r.ReadByte()
	if err != nil {
		if err == io.EOF {
			return key{kind: keyEsc}, nil
		}
		return key{}, err
	}
	switch b {
	case '[':
		return readCSI(r, fd)
	case 'O':
		return readSS3(r, fd)
	case '\r', '\n':
		return key{kind: keyAltEnter}, nil
	default:
		return key{kind: keyEsc}, nil
	}
}

// maxCSIBytes bounds one escape sequence. A paste that ends inside a sequence
// cannot keep the decoder reading forever.
const maxCSIBytes = 32

// readCSI consumes a CSI sequence and maps the arrows, Shift-Tab and the two
// bracketed-paste markers. Every other final byte is consumed as one key so its
// bytes cannot leak into the search field as text. A sequence that stops
// arriving is dropped as keyNone instead of blocking the frame until the next
// keypress.
func readCSI(r *bufio.Reader, fd int) (key, error) {
	var params [maxCSIBytes]byte
	n := 0
	for i := 0; i < maxCSIBytes; i++ {
		if r.Buffered() == 0 && !inputPending(fd, escapeWindowMS) {
			return key{kind: keyNone}, nil
		}
		b, err := r.ReadByte()
		if err != nil {
			if err == io.EOF {
				return key{kind: keyEsc}, nil
			}
			return key{}, err
		}
		if b >= 0x40 && b <= 0x7e {
			switch b {
			case 'A':
				return key{kind: keyUp}, nil
			case 'B':
				return key{kind: keyDown}, nil
			case 'C':
				return key{kind: keyRight}, nil
			case 'D':
				return key{kind: keyLeft}, nil
			case 'Z':
				return key{kind: keyShiftTab}, nil
			case '~':
				switch string(params[:n]) {
				case "200":
					return key{kind: keyPasteStart}, nil
				case "201":
					return key{kind: keyPasteEnd}, nil
				}
			}
			return key{kind: keyNone}, nil
		}
		if b < 0x20 || b > 0x3f {
			return key{kind: keyNone}, nil
		}
		params[n] = b
		n++
	}
	return key{kind: keyNone}, nil
}

func readSS3(r *bufio.Reader, fd int) (key, error) {
	if r.Buffered() == 0 && !inputPending(fd, escapeWindowMS) {
		return key{kind: keyNone}, nil
	}
	b, err := r.ReadByte()
	if err != nil {
		if err == io.EOF {
			return key{kind: keyEsc}, nil
		}
		return key{}, err
	}
	switch b {
	case 'A':
		return key{kind: keyUp}, nil
	case 'B':
		return key{kind: keyDown}, nil
	case 'C':
		return key{kind: keyRight}, nil
	case 'D':
		return key{kind: keyLeft}, nil
	}
	return key{kind: keyNone}, nil
}

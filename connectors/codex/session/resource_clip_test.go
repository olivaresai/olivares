// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestClipNeverBreaksACharacter pins the fix for a defect measured on 2026-08-19: clip sliced
// BYTES (`s[:maxRefLen]`), so a reference made of multibyte runes came out as invalid UTF-8.
//
// It is not a theoretical input. Three of resource.go's five call sites derive the ref from a
// FILE PATH — `path`, `file_path`, `filename` — which is exactly where non-ASCII appears in a
// real installation, and the result lands in an audit record.
//
// Measured with the former body: 400 CJK runes (1200 bytes) produced 203 bytes,
// utf8.ValidString == false, ending in RuneError.
func TestClipNeverBreaksACharacter(t *testing.T) {
	t.Parallel()

	// 3 bytes per rune: a byte cut at 200 ALWAYS lands mid-character.
	long := strings.Repeat("日", 400)
	got := clip(long)

	if !utf8.ValidString(got) {
		t.Fatalf("clip produced invalid UTF-8: %q", got)
	}
	if utf8.RuneCountInString(got) > maxRefLen+1 { // +1 for the ellipsis
		t.Fatalf("clip did not bound the reference: %d runes", utf8.RuneCountInString(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("a clipped reference must say so: %q", got)
	}

	// Positive control: a short reference is returned untouched, so the assertions above
	// cannot be satisfied by a function that always returns the same thing.
	if clip("Bash") != "Bash" {
		t.Fatal("a short reference must not be clipped")
	}

	// And the bound is counted in RUNES: 200 Japanese characters are 600 bytes and must not be
	// clipped any sooner than 200 Latin ones.
	exact := strings.Repeat("日", maxRefLen)
	if clip(exact) != exact {
		t.Fatal("the cap narrows with the alphabet: 200 Japanese runes were clipped, 200 Latin ones are not")
	}
}

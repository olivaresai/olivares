// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxRunNameRunes bounds a session's name.
const maxRunNameRunes = 200

// checkRunName is the one rule for every write of a session's name: a launch, a
// handoff, a child launch and a rename (run_name_archive.go) all pass through it,
// so a name the rename refuses is refused at creation as well. The name is already
// trimmed. A control, format (zero-width, direction overrides), line or paragraph
// separator or default-ignorable character has no use in a label, and with one a
// name can pass for another in a list.
func checkRunName(name string) error {
	if utf8.RuneCountInString(name) > maxRunNameRunes || strings.IndexFunc(name, invisibleInName) >= 0 {
		return badRequest("name must be at most 200 characters and hold no control or invisible characters")
	}
	return nil
}

// invisibleInName is a character a session's name refuses: a control, format or
// separator character, or one Unicode lists as ignorable or invisible by default
// (Hangul fillers, variation selectors), with which a name can look blank. The
// braille blank (U+2800) is a symbol that draws nothing, so it is refused too.
func invisibleInName(r rune) bool {
	return unicode.IsControl(r) || r == '\u2800' || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp,
		unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector)
}

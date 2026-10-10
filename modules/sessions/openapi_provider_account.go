// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "github.com/olivaresai/olivares/core/api/oas"

func sessionsPatchProviderAccountMetadataSchema() map[string]any {
	schema := sessionsClosureClosedObject(oas.Obj(
		"name", oas.Obj("type", "string", "description", "Renames the account. Omit to keep the name; null is refused. The name is checked exactly as given: lowercase ASCII, a letter first, then letters, digits or '-', at most 32 characters (422 otherwise). A name already used by another account of the same environment, of any driver or state, answers 409 and is never replaced. The reference, homes and launch configuration do not change. Repeating the current name is a no-op."),
		"display_name", oas.Obj("type", "string", "description", "Display label only. Omit to preserve the label; explicit null is refused. Surrounding whitespace is trimmed; the normalized label is at most 200 UTF-8 bytes and contains no control characters. Empty clears it. The reference, homes and launch configuration do not change. Repeating the current normalized label is a no-op."),
		"accent", oas.Obj("type", "string", "enum", oas.Enum("", "orange", "green", "amber", "red", "blue"), "description", "Display-only accent color. Omit to preserve it; empty clears it; null is refused. Color carries no state or authority."),
	))
	schema["minProperties"] = 1
	return schema
}

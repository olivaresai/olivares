// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

func sessionsPatchProviderAccountMetadataSchema() map[string]any {
	schema := sessionsClosureClosedObject(oaObj(
		"display_name", oaObj("type", "string", "description", "Display label only. Omit to preserve the label; explicit null is refused. Surrounding whitespace is trimmed; the normalized label is at most 200 UTF-8 bytes and contains no control characters. Empty clears it. The stable account name, reference, homes and launch configuration do not change. Repeating the current normalized label is a no-op."),
		"accent", oaObj("type", "string", "enum", oaEnum("", "orange", "green", "amber", "red", "blue"), "description", "Display-only accent color. Omit to preserve it; empty clears it; null is refused. Color carries no state or authority."),
	))
	schema["minProperties"] = 1
	return schema
}

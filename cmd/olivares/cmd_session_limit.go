// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "time"

// limitSentence is what a session says when its tool ended a turn on a usage or rate
// limit (the run's `limit`, from the tool's own result). The console says the same.
// A usage limit whose reset time has passed says nothing: the account works again.
func limitSentence(run map[string]any, now time.Time) string {
	limit, _ := run["limit"].(map[string]any)
	switch str(limit, "kind") {
	case "rate":
		return "Rate limited by the provider. Try again in a moment."
	case "usage":
		at, err := time.Parse(time.RFC3339, str(limit, "resets_at"))
		if err != nil {
			return "Out of quota."
		}
		if !at.After(now) {
			return "" // the limit has lifted; the next turn clears the fact
		}
		at = at.Local()
		layout := "15:04"
		if y, m, d := at.Date(); y != now.Year() || m != now.Month() || d != now.Day() {
			layout = "2006-01-02 15:04"
		}
		return "Out of quota until " + at.Format(layout)
	}
	return ""
}

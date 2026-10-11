// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourcesConfigDocsFailClosed(t *testing.T) {
	for _, locale := range []string{"", "de", "es", "fr", "ja", "ru", "zh"} {
		for _, page := range []string{"reference/configuration.md", "how-to/connect-a-source.md", "how-to/troubleshooting.md"} {
			path := filepath.Join("..", "..", "docs-site", "src", "content", "docs", locale, page)
			t.Run(filepath.Join(locale, page), func(t *testing.T) {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				text := strings.Join(strings.Fields(strings.ReplaceAll(string(data), "*", "")), " ")
				for _, stale := range []string{"warns and continues", "never aborts the boot", "never crashes on it", "does not crash on boot"} {
					if strings.Contains(text, stale) {
						t.Errorf("sources documentation still promises startup continues: %q", stale)
					}
				}
				for _, required := range []string{"OLIVARES_SOURCES_CONFIG", "`1`", "load sources operator config:", "refusing to start instead of silently omitting operator configuration"} {
					if !strings.Contains(text, required) {
						t.Errorf("sources documentation omits the startup failure contract: %q", required)
					}
				}
			})
		}
	}
}

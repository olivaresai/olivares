// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"encoding/json"
	"testing"
)

func TestI18nCatalogsHaveEnglishKeyParity(t *testing.T) {
	en := readLocaleCatalog(t, "en")
	for _, locale := range SupportedLocales() {
		got := readLocaleCatalog(t, locale)
		if len(got) != len(en) {
			t.Fatalf("%s keys = %d, want %d", locale, len(got), len(en))
		}
		for key := range en {
			if got[key] == "" {
				t.Fatalf("%s missing translation key %q", locale, key)
			}
		}
		for key := range got {
			if _, ok := en[key]; !ok {
				t.Fatalf("%s has extra translation key %q", locale, key)
			}
		}
	}
}

func TestTranslationFallbacks(t *testing.T) {
	if got := T("xx", "report.finops.title"); got != T("en", "report.finops.title") {
		t.Fatalf("unknown locale fallback = %q, want English", got)
	}
	if got := T("en", "missing.key"); got != "missing.key" {
		t.Fatalf("missing key fallback = %q, want key", got)
	}
}

func readLocaleCatalog(t *testing.T, locale string) map[string]string {
	t.Helper()
	data, err := i18nFS.ReadFile("i18n/" + locale + ".json")
	if err != nil {
		t.Fatalf("read locale %s: %v", locale, err)
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse locale %s: %v", locale, err)
	}
	return out
}

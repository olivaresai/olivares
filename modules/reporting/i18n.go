// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"embed"
	"encoding/json"
	"sync"
)

//go:embed i18n/*.json
var i18nFS embed.FS

var (
	i18nOnce    sync.Once
	i18nStrings map[string]map[string]string // locale → key → value
)

func loadI18n() {
	i18nOnce.Do(func() {
		i18nStrings = make(map[string]map[string]string)
		for _, locale := range []string{"en", "es", "zh", "ja", "de", "ru", "fr"} {
			data, err := i18nFS.ReadFile("i18n/" + locale + ".json")
			if err != nil {
				continue
			}
			var m map[string]string
			if err := json.Unmarshal(data, &m); err != nil {
				continue
			}
			i18nStrings[locale] = m
		}
	})
}

// T returns the translated string for key in locale, falling back to English.
func T(locale, key string) string {
	loadI18n()
	if m, ok := i18nStrings[locale]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	if m, ok := i18nStrings["en"]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return key
}

// SupportedLocales returns the list of supported locales.
func SupportedLocales() []string {
	return []string{"en", "es", "zh", "ja", "de", "ru", "fr"}
}

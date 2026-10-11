// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package envconfig reads engine environment values without caching them.
// Callers retain their published validation, ranges, and fallback policies.
package envconfig

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Get returns the process value unchanged, including whitespace.
func Get(key string) string { return os.Getenv(key) }

// Reader resolves process values, then an optional activation fallback.
// Source supplies an injected environment in loaders and tests; nil uses Get.
// An empty value falls through, matching the engine's activation precedence.
type Reader struct {
	Source   func(string) string
	Fallback func(string) string
}

func (r Reader) Get(key string) string {
	source := r.Source
	if source == nil {
		source = Get
	}
	if value := source(key); value != "" {
		return value
	}
	if r.Fallback != nil {
		return r.Fallback(key)
	}
	return ""
}

// Int parses a trimmed decimal integer. Only an empty value uses the default.
func (r Reader) Int(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(r.Get(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer", key)
	}
	return value, nil
}

// Bool accepts strconv.ParseBool spellings. Callers with legacy spellings
// (such as yes/on or only 1) must retain that policy instead.
func (r Reader) Bool(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(r.Get(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: invalid boolean", key)
	}
	return value, nil
}

// Duration parses a trimmed Go duration; range validation belongs to the caller.
func (r Reader) Duration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(r.Get(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration", key)
	}
	return value, nil
}

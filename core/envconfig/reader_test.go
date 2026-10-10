// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package envconfig

import (
	"strings"
	"testing"
	"time"
)

func TestReaderPrecedenceAndTypedValues(t *testing.T) {
	const key = "OLIVARES_TEST_READER"
	r := Reader{Fallback: func(string) string { return "17" }}
	t.Setenv(key, "")
	if got := r.Get(key); got != "17" {
		t.Fatalf("empty environment must use fallback: %q", got)
	}
	t.Setenv(key, " 23 ")
	if got := r.Get(key); got != " 23 " {
		t.Fatalf("raw value must preserve whitespace and override fallback: %q", got)
	}
	if got, err := r.Int(key, 3); err != nil || got != 23 {
		t.Fatalf("integer = %d, %v", got, err)
	}
	t.Setenv(key, "TRUE")
	if got, err := r.Bool(key, false); err != nil || !got {
		t.Fatalf("boolean = %v, %v", got, err)
	}
	t.Setenv(key, " 250ms ")
	if got, err := r.Duration(key, time.Second); err != nil || got != 250*time.Millisecond {
		t.Fatalf("duration = %v, %v", got, err)
	}
	for _, raw := range []string{"invalid", "999999999999999999999999999999999"} {
		t.Setenv(key, raw)
		if _, err := r.Int(key, 3); err == nil {
			t.Errorf("integer %q silently fell back", raw)
		}
		if _, err := r.Bool(key, false); err == nil {
			t.Errorf("boolean %q silently fell back", raw)
		}
		if _, err := r.Duration(key, time.Second); err == nil {
			t.Errorf("duration %q silently fell back", raw)
		}
	}
	r = Reader{}
	t.Setenv(key, " \t ")
	if got, err := r.Int(key, 3); err != nil || got != 3 {
		t.Fatalf("integer default = %d, %v", got, err)
	}
	if got, err := r.Bool(key, true); err != nil || !got {
		t.Fatalf("boolean default = %v, %v", got, err)
	}
	if got, err := r.Duration(key, time.Second); err != nil || got != time.Second {
		t.Fatalf("duration default = %v, %v", got, err)
	}
}

func TestReaderInjectedSourceAndSafeErrors(t *testing.T) {
	const key = "OLIVARES_TEST_READER"
	t.Setenv(key, "23")
	r := Reader{Source: func(string) string { return "17" }}
	if got, err := r.Int(key, 3); err != nil || got != 17 {
		t.Fatalf("injected integer = %d, %v", got, err)
	}
	const secret = "synthetic-private-value"
	r.Source = func(string) string { return secret }
	_, intErr := r.Int(key, 3)
	_, boolErr := r.Bool(key, false)
	_, durationErr := r.Duration(key, time.Second)
	for _, err := range []error{intErr, boolErr, durationErr} {
		if err == nil || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), secret) {
			t.Fatalf("error must identify the key without its value: %v", err)
		}
	}
}

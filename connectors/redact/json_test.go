// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package redact_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/redact"
)

func TestCleanJSONRedactsCompleteValuesAndPreservesReviewFacts(t *testing.T) {
	input := []byte(`{"password":"first second","token":["array secret"],"secret":{"key":"object secret"},"nested":[{"client_secret":"escaped \"suffix\""}],"path":"./scripts/read.go","count":9007199254740993,"command":"password='first second' && echo ./scripts/read.go","encoded":"{\"password\":\"nested string suffix\"}","key_block":"-----BEGIN PRIVATE ` + `KEY-----\nprivate body\n-----END PRIVATE ` + `KEY-----"}`)
	got, err := redact.CleanJSON(input)
	if err != nil || !json.Valid(got) {
		t.Fatal("review JSON is unavailable")
	}
	for _, secret := range []string{"first", "second", "array secret", "object secret", "escaped", "suffix", "private body", "nested string suffix"} {
		if strings.Contains(string(got), secret) {
			t.Fatalf("secret fragment %q survived", secret)
		}
	}
	for _, fact := range []string{`"path":"./scripts/read.go"`, `"count":9007199254740993`, `"token":"[REDACTED]"`, `"secret":"[REDACTED]"`, `"command":"[REDACTED]"`, `"encoded":"[REDACTED]"`} {
		if !strings.Contains(string(got), fact) {
			t.Fatalf("review fact %q unavailable", fact)
		}
	}
	for _, command := range []string{"password=first\\ second", "password='first\\\nsecond'", "password='first second\\", "password=ab", "password=first' second'", "password='first'\"second\""} {
		encoded, _ := json.Marshal(map[string]string{"command": command})
		cleaned, err := redact.CleanJSON(encoded)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"first", "second", "ab"} {
			if strings.Contains(string(cleaned), secret) {
				t.Fatal("escaped or short secret survived")
			}
		}
	}
	for _, malformed := range []string{`{"password":`, `{} {}`, `{"sk-abcdefghijklmnopqrstuvwx":"value"}`, `{"password='ab'":"value"}`} {
		if _, err := redact.CleanJSON([]byte(malformed)); err == nil {
			t.Fatal("invalid or unreviewable JSON accepted")
		}
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package redact_test

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/redact"
)

func TestReviewableJSONPreservesBehaviorAndMasksOnlyLiteralCredentials(t *testing.T) {
	input := []byte(`{"token":"sk-abcdefghijklmnopqrstuvwx","auth":"Bearer abcdefghijkl","command":"echo $(printf visible_operation)","count":9007199254740993,"nested":[{"api_key":"sk-abcdefghijklmnopqrstuvwx"}]}`)
	got, err := redact.ReviewableJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range []string{`"token":"[REDACTED]"`, `"auth":"[REDACTED]"`, `"api_key":"[REDACTED]"`, `echo $(printf visible_operation)`, `9007199254740993`} {
		if !strings.Contains(string(got), fact) {
			t.Fatalf("review fact unavailable: %s", fact)
		}
	}
	if strings.Contains(string(got), "sk-abcdefghijklmnopqrstuvwx") {
		t.Fatal("literal credential disclosed")
	}
	for _, raw := range []string{
		`{"command":"echo password=$(printf${IFS}hidden_operation)"}`,
		`{"token":"sk-abcdefghijklmnopqrstuvwx$(touch hidden_operation)"}`,
		`{"note":"before sk-abcdefghijklmnopqrstuvwx after"}`,
		`{"token":{"code":"echo hidden_operation"}}`,
		`{"token":["sk-abcdefghijklmnopqrstuvwx","echo hidden_operation"]}`,
		`{"token":"fixture-secret-only"}`,
		`{"command":"sk-abcdefghijklmnopqrstuvwx"}`,
		`{"token":"Bearer\n/tmp/hidden_operation"}`,
	} {
		if _, err := redact.ReviewableJSON([]byte(raw)); err == nil {
			t.Fatal("hidden or unrecognized argument accepted")
		}
	}
}

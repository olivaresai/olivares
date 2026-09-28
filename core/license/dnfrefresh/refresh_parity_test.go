// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dnfrefresh

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/license/connectv1"
)

// TestEveryClaimMismatchRefusesWithoutEchoingTheCredential: a claim that is not the local binding or
// the answer is ErrAnswer, and the refusal text never carries the credential.
func TestEveryClaimMismatchRefusesWithoutEchoingTheCredential(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"k":   func(c map[string]any) { c["k"] = "kid_8" },
		"d":   func(c map[string]any) { c["d"] = "dep_8" },
		"ep":  func(c map[string]any) { c["ep"] = 4 },
		"ctx": func(c map[string]any) { c["ctx"] = strings.Repeat("a", 32) },
		"cls": func(c map[string]any) { c["cls"] = "security-only" },
		"rsn": func(c map[string]any) { c["rsn"] = "term_ended" },
		"exp": func(c map[string]any) { c["exp"] = testNow.Unix() + 86401 },
		"iat": func(c map[string]any) { c["iat"] = testNow.Unix() + 86400 },
		"h":   func(c map[string]any) { c["h"] = "" },
		"lin": func(c map[string]any) { c["lin"] = map[string]any{"serial": "s"} },
	} {
		obj := answerFixture(t, edit, nil)
		cred, _ := obj["dnf_credential"].(string)
		a, err := CheckAnswer(obj, testBinding, testNow)
		if !errors.Is(err, ErrAnswer) {
			t.Errorf("claim %s: want ErrAnswer", name)
			continue
		}
		if a.Credential != "" || credentialLeaked(err.Error(), cred) {
			t.Errorf("claim %s: the refusal carries the credential", name)
		}
	}
	lifetime := answerFixture(t, func(c map[string]any) { c["exp"] = testNow.Unix() + 86701 },
		func(a map[string]any) { a["exp"] = testNow.Unix() + 86701 })
	cred, _ := lifetime["dnf_credential"].(string)
	_, err := CheckAnswer(lifetime, testBinding, testNow)
	if !errors.Is(err, ErrAnswer) {
		t.Fatal("lifetime past 86700 s was accepted")
	}
	if credentialLeaked(err.Error(), cred) {
		t.Fatal("the lifetime refusal carries the credential")
	}
}

func credentialLeaked(text, cred string) bool {
	if cred == "" {
		return false
	}
	if strings.Contains(text, cred) || strings.Contains(text, CredentialPrefix) {
		return true
	}
	mac := cred[len(cred)-43:]
	return strings.Contains(text, mac)
}

// TestCredentialGrammarBoundaries: the grammar allows 16 to 1024 payload bytes, which is at most
// 1073 bytes of credential, and refuses one byte outside either end.
func TestCredentialGrammarBoundaries(t *testing.T) {
	mac := strings.Repeat("M", 43)
	for _, tc := range []struct {
		cred string
		ok   bool
	}{
		{CredentialPrefix + strings.Repeat("A", 16) + "." + mac, true},
		{CredentialPrefix + strings.Repeat("A", 1024) + "." + mac, true},
		{CredentialPrefix + strings.Repeat("A", 15) + "." + mac, false},
		{CredentialPrefix + strings.Repeat("A", 1025) + "." + mac, false},
		{CredentialPrefix + strings.Repeat("A", 16) + "." + mac[:42], false},
		{CredentialPrefix + strings.Repeat("A", 16) + "." + mac + "M", false},
	} {
		if len(tc.cred) == 1073 && !tc.ok {
			t.Fatalf("the 1073-byte fixture is marked refused")
		}
		if got := validCredential(tc.cred); got != tc.ok {
			t.Errorf("validCredential(%d bytes) = %v, want %v", len(tc.cred), got, tc.ok)
		}
	}
}

// TestReasonBoundAcceptsTheWorkersLongReasons: the 67- and 93-byte reasons the Worker sends, and a
// reason of 200 bytes, pass. 201 bytes is ErrAnswer.
func TestReasonBoundAcceptsTheWorkersLongReasons(t *testing.T) {
	sec := func(reason string) map[string]any {
		return answerFixture(t,
			func(c map[string]any) {
				c["cls"], c["su"], c["rsn"] = "security-only", []any{"entitled-security"}, reason
			},
			func(a map[string]any) {
				a["class"], a["suites"], a["reason"] = "security-only", []any{"entitled-security"}, reason
			})
	}
	long := func(n int) string {
		r := "term_ended"
		for len(r) < n {
			r += " held_set_narrowed"
		}
		return r[:n-1] + "d"
	}
	for _, reason := range []string{
		"refunded_renewal refunded_expansion_excluded held_set_single_source",
		"authority_legacy_unverified lineage_unread refunded_expansion_excluded held_set_single_source",
		long(200),
	} {
		if _, err := CheckAnswer(sec(reason), testBinding, testNow); err != nil {
			t.Errorf("a %d-byte reason: %v", len(reason), err)
		}
	}
	if _, err := CheckAnswer(sec(long(201)), testBinding, testNow); !errors.Is(err, ErrAnswer) {
		t.Errorf("a 201-byte reason was accepted")
	}
}

type grammarRefusal struct {
	reason string
}

func (r *grammarRefusal) Error() string                    { return "refused" }
func (r *grammarRefusal) RefusalStatus() int               { return http.StatusForbidden }
func (r *grammarRefusal) RefusalCode() connectv1.ErrorCode { return connectv1.ErrAuthorityDenied }
func (r *grammarRefusal) RefusalReason() string            { return r.reason }

// TestRefusedReasonSetIsExactlyTheThreeR7Reasons: authority_denied is refused only for revoked,
// refunded and absent. Every other token of the reason grammar is unknown with no code.
func TestRefusedReasonSetIsExactlyTheThreeR7Reasons(t *testing.T) {
	for _, reason := range []string{"current", "outside_capacity", "term_ended", "refunded_renewal", "authority_ambiguous",
		"authority_legacy_unverified", "binding_denied", "provenance_unproven", "security_set_unresolved"} {
		if o, code := OutcomeOf(&grammarRefusal{reason: reason}); o != OutcomeUnknown || code != "" {
			t.Errorf("authority_denied/%s: %s/%q, want unknown with no code", reason, o, code)
		}
	}
	for _, reason := range []string{"revoked", "refunded", "absent"} {
		if o, code := OutcomeOf(&grammarRefusal{reason: reason}); o != OutcomeRefused || code != string(connectv1.ErrAuthorityDenied) {
			t.Errorf("authority_denied/%s: %s/%q, want refused/authority_denied", reason, o, code)
		}
	}
}

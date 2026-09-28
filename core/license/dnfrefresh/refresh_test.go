// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dnfrefresh

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license/connectv1"
)

const validCycle = "0123456789abcdef0123456789abcdef"

const testContext = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"

var testNow = time.Date(2026, 9, 27, 3, 0, 0, 0, time.UTC)

var testBinding = Binding{DeploymentID: "dep_7", PopKID: "kid_7", BindingEpoch: 3}

func testCredential(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	p := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte("test K_apt"))
	mac.Write([]byte(CredentialPrefix + p))
	return CredentialPrefix + p + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// answerFixture is a strict-parsed dnf-refresh answer. The credential schema stays
// olivares.ai/apt-download/v1; the audience and the answer field are the DNF channel's.
func answerFixture(t *testing.T, editClaims, editAnswer func(map[string]any)) map[string]any {
	t.Helper()
	exp := testNow.Unix() + 86400
	claims := map[string]any{
		"s": "olivares.ai/apt-download/v1", "aud": CredentialAudience, "cls": "current", "h": "sub_1",
		"d": "dep_7", "k": "kid_7", "ep": 3, "lin": map[string]any{"serial": "conn_production_dep_7_2", "issue_seq": 2},
		"set": "biz+reg", "su": []any{"entitled-security", "entitled-stable"}, "ctx": testContext, "rsn": "current",
		"iat": testNow.Unix(), "exp": exp, "jti": "jti_0001",
	}
	if editClaims != nil {
		editClaims(claims)
	}
	answer := map[string]any{
		"deployment_id": "dep_7", "binding_epoch": 3, "class": "current", "set": "biz+reg",
		"suites": []any{"entitled-security", "entitled-stable"}, "context": testContext,
		"dnf_credential": testCredential(t, claims), "exp": exp, "reason": "current",
	}
	if editAnswer != nil {
		editAnswer(answer)
	}
	data, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := connectv1.ParseStrictObject(data, 1<<16)
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

// TestAnswerWhoseAudienceIsNotTheDnfRootStaysPending: aud must be the DNF root. The APT root, and any
// other audience, is not a DNF credential. The failure is ErrAnswer, whose outcome is unknown, so the
// pending operation stays and the next cycle repeats it.
func TestAnswerWhoseAudienceIsNotTheDnfRootStaysPending(t *testing.T) {
	if CredentialAudience != "https://licenses.olivares.ai/dnf/v1/" {
		t.Fatalf("CredentialAudience = %q", CredentialAudience)
	}
	if _, err := CheckAnswer(answerFixture(t, nil, nil), testBinding, testNow); err != nil {
		t.Fatalf("the DNF root: %v", err)
	}
	for _, aud := range []string{
		"https://licenses.olivares.ai/apt/v1/",
		"https://licenses.olivares.ai/dnf/v1",
		"https://licenses.olivares.ai/dnf/v1/x",
		"http://127.0.0.1:8787/dnf/v1/",
		"https://LICENSES.olivares.ai/dnf/v1/",
	} {
		obj := answerFixture(t, func(c map[string]any) { c["aud"] = aud }, nil)
		a, err := CheckAnswer(obj, testBinding, testNow)
		if !errors.Is(err, ErrAnswer) {
			t.Errorf("aud %q: %v, want ErrAnswer", aud, err)
			continue
		}
		if o, code := OutcomeOf(err); o != OutcomeUnknown || code != "" {
			t.Errorf("aud %q: outcome %q/%q, want unknown so the operation stays pending", aud, o, code)
		}
		if a.Credential != "" || strings.Contains(err.Error(), obj["dnf_credential"].(string)) {
			t.Errorf("aud %q: the refusal returned or printed the credential", aud)
		}
	}
}

// TestCheckAnswerRequiresTheDnfCredentialField: the answer field is dnf_credential. apt_credential is
// not that field, and a credential that is not the download grammar is refused.
func TestCheckAnswerRequiresTheDnfCredentialField(t *testing.T) {
	obj := answerFixture(t, nil, func(a map[string]any) {
		a["apt_credential"] = a["dnf_credential"]
		delete(a, "dnf_credential")
	})
	if _, err := CheckAnswer(obj, testBinding, testNow); !errors.Is(err, ErrAnswer) {
		t.Fatalf("apt_credential in place of dnf_credential: %v, want ErrAnswer", err)
	}
	short := answerFixture(t, nil, func(a map[string]any) { a["dnf_credential"] = "oad1.short.mac" })
	if _, err := CheckAnswer(short, testBinding, testNow); !errors.Is(err, ErrAnswer) {
		t.Fatalf("a credential outside the grammar: %v, want ErrAnswer", err)
	}
}

// TestCredentialBoundIsAtMost1073Bytes: the grammar allows at most 1073 bytes
// (oad1. + 1024 + . + 43). One byte more is refused. The download schema stays
// olivares.ai/apt-download/v1.
func TestCredentialBoundIsAtMost1073Bytes(t *testing.T) {
	mac := strings.Repeat("M", 43)
	ok := CredentialPrefix + strings.Repeat("A", 1024) + "." + mac
	if len(ok) != 1073 {
		t.Fatalf("1073-byte fixture is %d bytes", len(ok))
	}
	over := CredentialPrefix + strings.Repeat("A", 1025) + "." + mac
	if len(over) != 1074 {
		t.Fatalf("1074-byte fixture is %d bytes", len(over))
	}
	accept := answerFixture(t, nil, func(a map[string]any) { a["dnf_credential"] = ok })
	// The payload is not claims, so the grammar may pass and the decode may fail. Either way a
	// 1074-byte value is not accepted. A value of 1073 bytes that is not claims fails closed too;
	// the bound itself is the length the grammar stops at, pinned by the two lengths above.
	if _, err := CheckAnswer(accept, testBinding, testNow); err == nil {
		t.Fatal("a 1073-byte payload that is not claims was accepted")
	}
	refuse := answerFixture(t, nil, func(a map[string]any) { a["dnf_credential"] = over })
	if _, err := CheckAnswer(refuse, testBinding, testNow); !errors.Is(err, ErrAnswer) {
		t.Fatalf("a 1074-byte credential: %v, want ErrAnswer", err)
	}
	obj := answerFixture(t, func(c map[string]any) { c["s"] = "olivares.ai/dnf-download/v1" }, nil)
	if _, err := CheckAnswer(obj, testBinding, testNow); !errors.Is(err, ErrAnswer) {
		t.Fatalf("a renamed download schema: %v, want ErrAnswer", err)
	}
}

type testRefusal struct {
	status int
	code   connectv1.ErrorCode
	reason string
}

func (r *testRefusal) Error() string                    { return fmt.Sprintf("refused %d %s", r.status, r.code) }
func (r *testRefusal) RefusalStatus() int               { return r.status }
func (r *testRefusal) RefusalCode() connectv1.ErrorCode { return r.code }
func (r *testRefusal) RefusalReason() string            { return r.reason }

// TestReasonRevokedIsRefusedAndEndsTheOperation: authority_denied with reason revoked is refused.
// Refused is the outcome on which the command ends the pending operation. refunded and absent are
// the other two reasons of that rule; any other reason, or none, stays unknown and pending.
func TestReasonRevokedIsRefusedAndEndsTheOperation(t *testing.T) {
	for _, reason := range []string{"revoked", "refunded", "absent"} {
		o, code := OutcomeOf(&testRefusal{status: http.StatusForbidden, code: connectv1.ErrAuthorityDenied, reason: reason})
		if o != OutcomeRefused || code != string(connectv1.ErrAuthorityDenied) {
			t.Errorf("authority_denied/%s: %q/%q, want refused/authority_denied", reason, o, code)
		}
	}
	for _, reason := range []string{"", "provenance_unproven", "Revoked", "revoked refunded"} {
		o, code := OutcomeOf(&testRefusal{status: http.StatusForbidden, code: connectv1.ErrAuthorityDenied, reason: reason})
		if o != OutcomeUnknown || code != "" {
			t.Errorf("authority_denied/%q: %q/%q, want unknown with no code", reason, o, code)
		}
	}
	o, code := OutcomeOf(fmt.Errorf("check: %w", ErrAnswer))
	if o != OutcomeUnknown || code != "" {
		t.Fatalf("a failed answer check: %q/%q, want unknown", o, code)
	}
}

// TestPendingDnfRefreshDoesNotBlockAnotherConnectOperation: a pending dnf-refresh is replaced by
// bind, licence refresh, rotate, recover, reactivate, delete and by apt-refresh. A repeat of
// dnf-refresh is not replaced. A pending operation that is not a download refresh is not replaced.
func TestPendingDnfRefreshDoesNotBlockAnotherConnectOperation(t *testing.T) {
	for _, next := range []string{"bind", "refresh", "rotate", "recover", "reactivate", "delete", connectv1.OpAptRefresh} {
		if !Supersedes(connectv1.OpDnfRefresh, next) {
			t.Errorf("pending dnf-refresh blocked %s", next)
		}
	}
	if Supersedes(connectv1.OpDnfRefresh, connectv1.OpDnfRefresh) {
		t.Fatal("a repeat of dnf-refresh must keep the stored result")
	}
	if Supersedes(connectv1.OpAptRefresh, connectv1.OpAptRefresh) {
		t.Fatal("a repeat of apt-refresh must keep the stored result")
	}
	if !Supersedes(connectv1.OpAptRefresh, connectv1.OpDnfRefresh) {
		t.Fatal("dnf-refresh must replace a pending apt-refresh")
	}
	for _, pending := range []string{"bind", "refresh", "rotate", "recover", "reactivate", "delete"} {
		if Supersedes(pending, connectv1.OpDnfRefresh) || Supersedes(pending, connectv1.OpAptRefresh) {
			t.Errorf("pending %s must not be replaced by a download refresh", pending)
		}
	}
}

// TestCheckCycleRefusesEveryOtherForm: --cycle is exactly 32 lowercase hex characters.
func TestCheckCycleRefusesEveryOtherForm(t *testing.T) {
	if err := CheckCycle(validCycle); err != nil {
		t.Fatalf("valid cycle: %v", err)
	}
	for _, cycle := range []string{validCycle[:31], validCycle + "0", "0123456789ABCDEF0123456789abcdef", "", validCycle[:31] + "\n"} {
		err := CheckCycle(cycle)
		if !errors.Is(err, ErrCycleInvalid) || !strings.HasPrefix(err.Error(), CodeCycleInvalid+": ") {
			t.Errorf("CheckCycle(%q) = %v, want %s", cycle, err, CodeCycleInvalid)
		}
	}
}

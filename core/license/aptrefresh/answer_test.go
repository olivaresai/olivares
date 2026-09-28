// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package aptrefresh

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license/connectv1"
)

var testNow = time.Date(2026, 9, 27, 3, 0, 0, 0, time.UTC)

var testBinding = Binding{DeploymentID: "dep_7", PopKID: "kid_7", BindingEpoch: 3}

const testContext = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"

// testCredential encodes claims as a download credential. The MAC key is a test value: the product
// cannot verify the MAC (only the service holds K_apt), so it checks the grammar and the decoded claims.
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

// answerFixture is the strict-parsed answer the service sends for testBinding at testNow (Interface Q3 r2
// §3.1.1.1 step 7 and §3.1.1.4). editClaims changes the credential's claims before it is encoded;
// editAnswer changes the answer after.
func answerFixture(t *testing.T, editClaims, editAnswer func(map[string]any)) map[string]any {
	t.Helper()
	exp := testNow.Unix() + 86400
	claims := map[string]any{
		"s": "olivares.ai/apt-download/v1", "aud": "https://licenses.olivares.ai/apt/v1/", "cls": "current", "h": "sub_1",
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
		"apt_credential": testCredential(t, claims), "exp": exp, "reason": "current",
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

// TestCheckAnswerAcceptsTheServiceAnswer is the control of every refusal below.
func TestCheckAnswerAcceptsTheServiceAnswer(t *testing.T) {
	obj := answerFixture(t, nil, nil)
	a, err := CheckAnswer(obj, testBinding, testNow)
	if err != nil {
		t.Fatalf("CheckAnswer: %v", err)
	}
	want := Answer{Class: "current", Set: "biz+reg", Suites: []string{"entitled-security", "entitled-stable"},
		Context: testContext, Credential: obj["apt_credential"].(string), Exp: testNow.Unix() + 86400}
	if a.Class != want.Class || a.Set != want.Set || strings.Join(a.Suites, " ") != strings.Join(want.Suites, " ") ||
		a.Context != want.Context || a.Credential != want.Credential || a.Exp != want.Exp {
		t.Fatalf("CheckAnswer = %+v, want %+v", a, want)
	}
}

// TestCheckAnswerRefusesEveryFailedCheck: the product's answer checks before the handoff (Interface Q3 r2
// §3.1.4.2): exact fields; deployment_id and binding_epoch equal the binding; the credential grammar; the
// decoded aud, d, k and ep equal the local binding and ctx equals context; exp − now ≤ 86700 s. Each failure
// is ErrAnswer, whose outcome is unknown: the pending operation stays (TestOutcomeOfEveryRefreshPath).
func TestCheckAnswerRefusesEveryFailedCheck(t *testing.T) {
	setCred := func(f func(string) string) func(map[string]any) {
		return func(a map[string]any) { a["apt_credential"] = f(a["apt_credential"].(string)) }
	}
	parts := func(c string) []string { return strings.Split(c, ".") }
	cases := []struct {
		name         string
		claims, ansr func(map[string]any)
	}{
		// exact fields
		{"an extra field ota", nil, func(a map[string]any) { a["ota"] = "bearer" }},
		{"an extra field credential", nil, func(a map[string]any) { a["credential"] = "licence" }},
		{"a missing field reason", nil, func(a map[string]any) { delete(a, "reason") }},
		{"a missing field apt_credential", nil, func(a map[string]any) { delete(a, "apt_credential") }},
		{"suites that is not an array", nil, func(a map[string]any) { a["suites"] = "entitled-security" }},
		// deployment_id and binding_epoch
		{"another deployment_id", nil, func(a map[string]any) { a["deployment_id"] = "dep_8" }},
		{"another binding_epoch", nil, func(a map[string]any) { a["binding_epoch"] = 4 }},
		// the credential grammar ^oad1\.[A-Za-z0-9_-]{16,1024}\.[A-Za-z0-9_-]{43}$
		{"another prefix", nil, setCred(func(c string) string { return "oad2." + strings.TrimPrefix(c, CredentialPrefix) })},
		{"a 42-character MAC", nil, setCred(func(c string) string { return c[:len(c)-1] })},
		{"a 44-character MAC", nil, setCred(func(c string) string { return c + "A" })},
		{"a padded payload", nil, setCred(func(c string) string { p := parts(c); return p[0] + "." + p[1] + "=." + p[2] })},
		{"a 15-character payload", nil, setCred(func(c string) string { return "oad1.eyJzIjoieCJ9MDA." + parts(c)[2] })},
		{"a 1025-character payload", nil, setCred(func(c string) string { return "oad1." + strings.Repeat("A", 1025) + "." + parts(c)[2] })},
		{"a space", nil, setCred(func(c string) string { return c + " " })},
		{"a payload that is not JSON", nil, setCred(func(c string) string {
			return "oad1." + base64.RawURLEncoding.EncodeToString([]byte("not a json object at all")) + "." + parts(c)[2]
		})},
		// decoded claims against the local binding and the answer
		{"another audience", func(c map[string]any) { c["aud"] = "https://evil.example/apt/v1/" }, nil},
		{"the audience of another path", func(c map[string]any) { c["aud"] = "https://licenses.olivares.ai/download/" }, nil},
		{"another deployment d", func(c map[string]any) { c["d"] = "dep_8" }, nil},
		{"another pop kid k", func(c map[string]any) { c["k"] = "kid_8" }, nil},
		{"another epoch ep", func(c map[string]any) { c["ep"] = 4 }, nil},
		{"ctx unlike context", func(c map[string]any) { c["ctx"] = "ffffffffffffffffffffffffffffffff" }, nil},
		{"context unlike ctx", nil, func(a map[string]any) { a["context"] = "ffffffffffffffffffffffffffffffff" }},
		{"another schema s", func(c map[string]any) { c["s"] = "olivares.ai/apt-download/v2" }, nil},
		{"a licence claim c", func(c map[string]any) { c["c"] = "licence" }, nil},
		{"a missing claim jti", func(c map[string]any) { delete(c, "jti") }, nil},
		{"cls unlike class", func(c map[string]any) { c["cls"] = "security-only" }, nil},
		{"set unlike the answer's set", func(c map[string]any) { c["set"] = "biz" }, nil},
		{"su unlike suites", func(c map[string]any) { c["su"] = []any{"entitled-security"} }, nil},
		{"exp unlike the answer's exp", func(c map[string]any) { c["exp"] = testNow.Unix() + 3600 }, nil},
		// the lifetime bound and the class rules
		{"exp − now of 86701 s", func(c map[string]any) { c["exp"] = testNow.Unix() + 86701 },
			func(a map[string]any) { a["exp"] = testNow.Unix() + 86701 }},
		{"an expired credential", func(c map[string]any) { c["exp"] = testNow.Unix() },
			func(a map[string]any) { a["exp"] = testNow.Unix() }},
		{"class current without entitled-stable", func(c map[string]any) { c["su"] = []any{"entitled-security"} },
			func(a map[string]any) { a["suites"] = []any{"entitled-security"} }},
		{"class security-only with entitled-stable", func(c map[string]any) { c["cls"] = "security-only" },
			func(a map[string]any) { a["class"] = "security-only" }},
		{"an unknown class", func(c map[string]any) { c["cls"] = "feature" }, func(a map[string]any) { a["class"] = "feature" }},
		{"unsorted suites", func(c map[string]any) { c["su"] = []any{"entitled-stable", "entitled-security"} },
			func(a map[string]any) { a["suites"] = []any{"entitled-stable", "entitled-security"} }},
		{"a set with a path character", func(c map[string]any) { c["set"] = "biz/../x" }, func(a map[string]any) { a["set"] = "biz/../x" }},
		{"an upper-case context", func(c map[string]any) { c["ctx"] = strings.ToUpper(testContext) },
			func(a map[string]any) { a["context"] = strings.ToUpper(testContext) }},
	}
	for _, tc := range cases {
		obj := answerFixture(t, tc.claims, tc.ansr)
		a, err := CheckAnswer(obj, testBinding, testNow)
		if !errors.Is(err, ErrAnswer) {
			t.Errorf("%s: CheckAnswer = %+v, %v; want ErrAnswer", tc.name, a, err)
			continue
		}
		if o, code := OutcomeOf(err); o != OutcomeUnknown || code != "" {
			t.Errorf("%s: outcome %q/%q, want unknown", tc.name, o, code)
		}
		if a.Credential != "" {
			t.Errorf("%s: a refused answer still returned its credential", tc.name)
		}
	}
}

// TestCheckAnswerAcceptsTheLifetimeBoundary: exp − now of exactly 86700 s is inside the bound, and each
// class carries its own suites.
func TestCheckAnswerAcceptsTheLifetimeBoundary(t *testing.T) {
	exp := testNow.Unix() + 86700
	obj := answerFixture(t, func(c map[string]any) { c["exp"] = exp }, func(a map[string]any) { a["exp"] = exp })
	if _, err := CheckAnswer(obj, testBinding, testNow); err != nil {
		t.Fatalf("exp − now = 86700 s: %v", err)
	}
	for _, class := range []string{"security-only", "security-unresolved"} {
		obj := answerFixture(t, func(c map[string]any) { c["cls"], c["su"], c["rsn"] = class, []any{"entitled-security"}, "term_ended" },
			func(a map[string]any) {
				a["class"], a["suites"], a["reason"] = class, []any{"entitled-security"}, "term_ended"
			})
		if _, err := CheckAnswer(obj, testBinding, testNow); err != nil {
			t.Errorf("class %s: %v", class, err)
		}
	}
}

// TestCredentialGrammarBoundaries: the grammar ^oad1\.[A-Za-z0-9_-]{16,1024}\.[A-Za-z0-9_-]{43}$ at its
// edges (it is written out in code because RE2 bounds a repeat count at 1000).
func TestCredentialGrammarBoundaries(t *testing.T) {
	mac := strings.Repeat("M", 43)
	for _, tc := range []struct {
		cred string
		ok   bool
	}{
		{"oad1." + strings.Repeat("A", 16) + "." + mac, true},
		{"oad1." + strings.Repeat("A", 1024) + "." + mac, true},
		{"oad1." + strings.Repeat("-_09az", 3) + "." + mac, true},
		{"oad1." + strings.Repeat("A", 15) + "." + mac, false},
		{"oad1." + strings.Repeat("A", 1025) + "." + mac, false},
		{"oad1." + strings.Repeat("A", 16) + "." + mac[:42], false},
		{"oad1." + strings.Repeat("A", 16) + "." + mac + "M", false},
		{"oad1." + strings.Repeat("A", 15) + "+." + mac, false},
		{"oad1." + strings.Repeat("A", 16) + "." + mac + ".x", false},
		{"OAD1." + strings.Repeat("A", 16) + "." + mac, false},
		{"oad1" + strings.Repeat("A", 17) + "." + mac, false},
	} {
		if got := validCredential(tc.cred); got != tc.ok {
			t.Errorf("validCredential(%d bytes %.12q…) = %v, want %v", len(tc.cred), tc.cred, got, tc.ok)
		}
	}
}

// The reasons Q3-S1 sends for an issued answer (read-r1/xslice-run.txt): a class reason, then set reasons,
// one space apart. The product bounds the answer's reason as the Worker's claim bounds rsn, 200 bytes, and
// requires reason == rsn; it does not repeat the Worker's token grammar.
const (
	reasonRefundedRenewal = "refunded_renewal refunded_expansion_excluded held_set_single_source"                           // 67 bytes, the QD-3b P6 case
	reasonLongest         = "authority_legacy_unverified lineage_unread refunded_expansion_excluded held_set_single_source" // 93 bytes, the longest reachable
)

// reasonOf returns a reason of exactly n bytes made of reason tokens.
func reasonOf(n int) string {
	r := "term_ended"
	for len(r) < n {
		r += " held_set_narrowed"
	}
	return r[:n-1] + "d"
}

func securityAnswer(t *testing.T, class, reason string) map[string]any {
	t.Helper()
	return answerFixture(t,
		func(c map[string]any) { c["cls"], c["su"], c["rsn"] = class, []any{"entitled-security"}, reason },
		func(a map[string]any) {
			a["class"], a["suites"], a["reason"] = class, []any{"entitled-security"}, reason
		})
}

// TestCheckAnswerAcceptsTheWorkersLongReasons (F1): the 67- and 93-byte reasons Q3-S1 sends, and a reason
// at the 200-byte claim bound, pass with rsn equal to reason.
func TestCheckAnswerAcceptsTheWorkersLongReasons(t *testing.T) {
	for _, tc := range []struct{ class, reason string }{
		{"security-only", reasonRefundedRenewal},
		{"security-unresolved", reasonLongest},
		{"security-only", reasonOf(200)},
	} {
		if _, err := CheckAnswer(securityAnswer(t, tc.class, tc.reason), testBinding, testNow); err != nil {
			t.Errorf("a %d-byte reason: %v", len(tc.reason), err)
		}
	}
}

// TestCheckAnswerRefusesAReasonPastTheClaimBound (F1): a reason past 200 bytes is ErrAnswer, even with rsn
// equal to it.
func TestCheckAnswerRefusesAReasonPastTheClaimBound(t *testing.T) {
	r := reasonOf(201)
	if _, err := CheckAnswer(securityAnswer(t, "security-only", r), testBinding, testNow); !errors.Is(err, ErrAnswer) {
		t.Fatalf("a %d-byte reason: %v, want ErrAnswer", len(r), err)
	}
}

// TestCheckAnswerAudienceIsTheLiteral (F2): aud is the adapter's URI root, the literal
// https://licenses.olivares.ai/apt/v1/ of r2 §3.1.1.4, whatever origin the deployment is bound through; any
// other aud, the bound origin's own included, is ErrAnswer. The binding below names no origin: the product
// does not consult one.
func TestCheckAnswerAudienceIsTheLiteral(t *testing.T) {
	b := Binding{DeploymentID: "dep_7", PopKID: "kid_7", BindingEpoch: 3}
	if _, err := CheckAnswer(answerFixture(t, nil, nil), b, testNow); err != nil {
		t.Fatalf("aud https://licenses.olivares.ai/apt/v1/: %v", err)
	}
	for _, aud := range []string{
		"/apt/v1/",
		"http://127.0.0.1:8787/apt/v1/",
		"https://staging.licenses.olivares.ai/apt/v1/",
		"https://licenses.olivares.ai/apt/v1",
		"https://licenses.olivares.ai/apt/v1/x",
		"https://LICENSES.olivares.ai/apt/v1/",
		"http://licenses.olivares.ai/apt/v1/",
	} {
		obj := answerFixture(t, func(c map[string]any) { c["aud"] = aud }, nil)
		if _, err := CheckAnswer(obj, b, testNow); !errors.Is(err, ErrAnswer) {
			t.Errorf("aud %q: %v, want ErrAnswer", aud, err)
		}
	}
}

// rawCredential encodes payload bytes as a credential with a fixed MAC, for claims no JSON encoder writes.
func rawCredential(payload []byte) string {
	return CredentialPrefix + base64.RawURLEncoding.EncodeToString(payload) + "." + strings.Repeat("M", 43)
}

// nonCanonical sets an unused low bit in the last character of an unpadded base64url string whose length
// leaves unused bits, so it decodes leniently but is not canonical.
func nonCanonical(t *testing.T, s string) string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	if len(s)%4 == 0 {
		t.Fatalf("a %d-character payload has no unused bits", len(s))
	}
	i := strings.IndexByte(alphabet, s[len(s)-1])
	return s[:len(s)-1] + string(alphabet[i|1])
}

// TestCheckAnswerRefusesTheProbedClaimDefects (F5): the read's probe cases, each ErrAnswer (outcome unknown)
// with no credential returned and none in the error text.
func TestCheckAnswerRefusesTheProbedClaimDefects(t *testing.T) {
	setCred := func(cred string) func(map[string]any) { return func(a map[string]any) { a["apt_credential"] = cred } }
	valid := answerFixture(t, nil, nil)["apt_credential"].(string)
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(valid, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	// A payload whose length is not a multiple of 3 leaves unused bits in its last base64url character.
	for len(payload)%3 == 0 {
		payload = []byte(strings.Replace(string(payload), `"jti":"jti_`, `"jti":"jti_x`, 1))
	}
	p64 := base64.RawURLEncoding.EncodeToString(payload)
	cases := []struct {
		name         string
		claims, ansr func(map[string]any)
	}{
		{"rsn unlike reason", func(c map[string]any) { c["rsn"] = "term_ended" }, nil},
		{"iat equal to exp", func(c map[string]any) { c["iat"] = testNow.Unix() + 86400 }, nil},
		{"iat after exp", func(c map[string]any) { c["iat"] = testNow.Unix() + 86401 }, nil},
		{"an empty holder h", func(c map[string]any) { c["h"] = "" }, nil},
		{"lin with an extra field", func(c map[string]any) {
			c["lin"] = map[string]any{"serial": "conn_production_dep_7_2", "issue_seq": 2, "epoch": 3}
		}, nil},
		{"lin.issue_seq 0", func(c map[string]any) { c["lin"] = map[string]any{"serial": "conn_production_dep_7_2", "issue_seq": 0} }, nil},
		{"an empty lin.serial", func(c map[string]any) { c["lin"] = map[string]any{"serial": "", "issue_seq": 2} }, nil},
		{"a missing claim h", func(c map[string]any) { delete(c, "h") }, nil},
		{"a missing claim lin", func(c map[string]any) { delete(c, "lin") }, nil},
		{"a missing claim s", func(c map[string]any) { delete(c, "s") }, nil},
		{"a missing claim iat", func(c map[string]any) { delete(c, "iat") }, nil},
		{"a duplicated claim d", nil, setCred(rawCredential([]byte(strings.Replace(string(payload), `"d":"dep_7"`, `"d":"dep_7","d":"dep_7"`, 1))))},
		{"non-canonical base64url", nil, setCred(CredentialPrefix + nonCanonical(t, p64) + "." + strings.Repeat("M", 43))},
	}
	if !strings.Contains(string(payload), `"d":"dep_7"`) {
		t.Fatalf("the fixture payload no longer names d as expected: %s", payload)
	}
	for _, tc := range cases {
		obj := answerFixture(t, tc.claims, tc.ansr)
		a, err := CheckAnswer(obj, testBinding, testNow)
		if !errors.Is(err, ErrAnswer) {
			t.Errorf("%s: CheckAnswer = %v, want ErrAnswer", tc.name, err)
			continue
		}
		if o, code := OutcomeOf(err); o != OutcomeUnknown || code != "" || a.Credential != "" {
			t.Errorf("%s: outcome %q/%q, credential returned %v", tc.name, o, code, a.Credential != "")
		}
		if cred, _ := obj["apt_credential"].(string); strings.Contains(err.Error(), cred) {
			t.Errorf("%s: the error text carries the credential", tc.name)
		}
	}
}

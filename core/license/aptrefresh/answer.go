// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package aptrefresh

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/license/connectv1"
)

// CredentialPrefix starts every download credential. A download credential is not a licence.
const CredentialPrefix = "oad1."

// credentialSchema is the claim s of a download credential.
const credentialSchema = "olivares.ai/apt-download/v1"

// CredentialAudience is the claim aud of every download credential: the adapter's URI root, fixed by
// Interface Q3 r2 §3.1.1.4 and minted as a constant by the Worker whatever origin the deployment is bound
// through. It is compared as this literal, never derived from the bound origin.
const CredentialAudience = "https://licenses.olivares.ai/apt/v1/"

// maxReasonBytes bounds the answer's reason as the Worker's claim grammar bounds rsn (200 bytes); its
// longest issued reason is 93 bytes. The product requires reason == rsn and does not repeat the grammar.
const maxReasonBytes = 200

// maxLifetimeSeconds bounds exp − now: the credential's 86400 s plus 300 s of clock skew.
const maxLifetimeSeconds = 86700

// ErrAnswer is an apt-refresh answer that failed the product's checks. Its outcome is unknown: the
// pending operation stays and the next run repeats it. Its text never carries an answer value.
var ErrAnswer = errors.New("aptrefresh: the apt-refresh answer failed its checks")

// setPattern is a canonical set: lowercase codes joined by +, as biz+ids+reg.
var setPattern = regexp.MustCompile(`^[a-z]+(\+[a-z]+)*$`)

// validCredential is the grammar of a download credential, ^oad1\.[A-Za-z0-9_-]{16,1024}\.[A-Za-z0-9_-]{43}$
// (Interface Q3 r2 §3.1.1.4). It is written out because RE2 bounds a repeat count at 1000.
func validCredential(s string) bool {
	parts := strings.Split(s, ".")
	return len(parts) == 3 && parts[0]+"." == CredentialPrefix &&
		len(parts[1]) >= 16 && len(parts[1]) <= 1024 && base64URLAlphabet(parts[1]) &&
		len(parts[2]) == 43 && base64URLAlphabet(parts[2])
}

func base64URLAlphabet(s string) bool {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// answerFields are exactly the fields of the service's answer (Interface Q3 r2 §3.1.1.1 step 7).
var answerFields = []string{"deployment_id", "binding_epoch", "class", "set", "suites", "context", "apt_credential", "exp", "reason"}

// claimFields are exactly the claims of a download credential (Interface Q3 r2 §3.1.1.4).
var claimFields = []string{"s", "aud", "cls", "h", "d", "k", "ep", "lin", "set", "su", "ctx", "rsn", "iat", "exp", "jti"}

// classSuites are the suites each class receives (Interface Q3 r3 §3.1.1.2).
var classSuites = map[string]string{
	"current":             "entitled-security entitled-stable",
	"security-only":       "entitled-security",
	"security-unresolved": "entitled-security",
}

// Binding is the local binding the answer must describe.
type Binding struct {
	DeploymentID string
	PopKID       string
	BindingEpoch int64
}

// Answer is an answer that passed every check.
type Answer struct {
	Class      string
	Set        string
	Suites     []string
	Context    string
	Credential string // never printed
	Exp        int64
}

func refuse(format string, args ...any) (Answer, error) {
	return Answer{}, fmt.Errorf("%w: "+format, append([]any{ErrAnswer}, args...)...)
}

// CheckAnswer checks a strict-parsed apt-refresh answer against the local binding at now, before any
// handoff is written (Interface Q3 r2 §3.1.4.2): exactly the answer's fields; deployment_id and
// binding_epoch equal the binding; the credential's grammar; its decoded claims exactly the credential's,
// with aud equal to CredentialAudience, d, k and ep equal to the local binding and ctx equal to context;
// and exp − now ≤ 86700 s.
// The product cannot verify the credential's MAC (only the service holds its key), so it also requires
// the claims to agree with the answer's class, set, suites, reason and exp, and the credential to be
// unexpired. A failure is ErrAnswer.
func CheckAnswer(obj map[string]any, b Binding, now time.Time) (Answer, error) {
	if err := connectv1.RequireExactFields(obj, answerFields, nil); err != nil {
		return refuse("%v", err)
	}
	var a Answer
	dep, err := connectv1.String(obj, "deployment_id", 80)
	if err != nil || dep != b.DeploymentID {
		return refuse("the answer names another deployment")
	}
	epoch, err := connectv1.Int(obj, "binding_epoch", 1)
	if err != nil || epoch != b.BindingEpoch {
		return refuse("the answer names another binding epoch")
	}
	if a.Class, err = connectv1.String(obj, "class", 32); err != nil || classSuites[a.Class] == "" {
		return refuse("the answer's class is not a download class")
	}
	if a.Set, err = connectv1.String(obj, "set", maxSetBytes); err != nil || !setPattern.MatchString(a.Set) {
		return refuse("the answer's set is not a canonical set")
	}
	if a.Suites, err = stringList(obj["suites"]); err != nil || strings.Join(a.Suites, " ") != classSuites[a.Class] {
		return refuse("the answer's suites are not those of class %s", a.Class)
	}
	if a.Context, err = connectv1.String(obj, "context", 32); err != nil || !id128Pattern.MatchString(a.Context) {
		return refuse("the answer's context is not 32 lowercase hex characters")
	}
	reason, err := connectv1.String(obj, "reason", maxReasonBytes)
	if err != nil {
		return refuse("the answer's reason is not a string of at most %d bytes", maxReasonBytes)
	}
	if a.Exp, err = connectv1.Int(obj, "exp", 1); err != nil {
		return refuse("the answer's exp is not an integer")
	}
	if a.Credential, err = connectv1.String(obj, "apt_credential", 1100); err != nil || !validCredential(a.Credential) {
		return refuse("the credential does not match the download credential grammar")
	}
	claims, err := decodeClaims(a.Credential)
	if err != nil {
		return refuse("the credential's claims do not decode: %v", err)
	}
	if err := checkClaims(claims, b, a, reason); err != nil {
		return refuse("%v", err)
	}
	if left := a.Exp - now.Unix(); left <= 0 || left > maxLifetimeSeconds {
		return refuse("exp − now is %d s, outside (0, %d]", left, maxLifetimeSeconds)
	}
	return a, nil
}

// decodeClaims reads the payload of a credential that matched the grammar.
func decodeClaims(credential string) (map[string]any, error) {
	parts := strings.Split(credential, ".")
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("the payload is not unpadded base64url")
	}
	claims, err := connectv1.ParseStrictObject(payload, len(payload))
	if err != nil {
		return nil, errors.New("the payload is not a strict JSON object")
	}
	if err := connectv1.RequireExactFields(claims, claimFields, nil); err != nil {
		return nil, err
	}
	return claims, nil
}

// checkClaims compares the decoded claims with the local binding and the answer.
func checkClaims(claims map[string]any, b Binding, a Answer, reason string) error {
	str := func(k string) string { s, _ := claims[k].(string); return s }
	num := func(k string) (int64, bool) { n, ok := claims[k].(int64); return n, ok }
	switch {
	case str("s") != credentialSchema:
		return errors.New("the credential is not a download credential")
	case str("aud") != CredentialAudience:
		return errors.New("the credential's audience is not the APT adapter's root")
	case str("d") != b.DeploymentID:
		return errors.New("the credential names another deployment")
	case str("k") != b.PopKID:
		return errors.New("the credential names another PoP key")
	case str("ctx") != a.Context:
		return errors.New("the credential's context is not the answer's")
	case str("cls") != a.Class || str("set") != a.Set || str("rsn") != reason:
		return errors.New("the credential's class, set or reason is not the answer's")
	case str("h") == "" || str("jti") == "":
		return errors.New("the credential names no holder or no id")
	}
	if ep, ok := num("ep"); !ok || ep != b.BindingEpoch {
		return errors.New("the credential names another binding epoch")
	}
	if exp, ok := num("exp"); !ok || exp != a.Exp {
		return errors.New("the credential's exp is not the answer's")
	}
	if iat, ok := num("iat"); !ok || iat >= a.Exp {
		return errors.New("the credential's iat is not before its exp")
	}
	if su, err := stringList(claims["su"]); err != nil || !slices.Equal(su, a.Suites) {
		return errors.New("the credential's suites are not the answer's")
	}
	lin, ok := claims["lin"].(map[string]any)
	if !ok || connectv1.RequireExactFields(lin, []string{"serial", "issue_seq"}, nil) != nil {
		return errors.New("the credential's lineage is not {serial, issue_seq}")
	}
	if _, err := connectv1.String(lin, "serial", 256); err != nil {
		return errors.New("the credential's lineage names no serial")
	}
	if _, err := connectv1.Int(lin, "issue_seq", 1); err != nil {
		return errors.New("the credential's lineage names no issue sequence")
	}
	return nil
}

// stringList reads a JSON array of non-empty strings.
func stringList(v any) ([]string, error) {
	items, ok := v.([]any)
	if !ok {
		return nil, errors.New("not an array")
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, ok := it.(string)
		if !ok || s == "" {
			return nil, errors.New("not an array of strings")
		}
		out = append(out, s)
	}
	return out, nil
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package dnfrefresh is the product side of the connect-v1 operation dnf-refresh: the rules of
// `olivares license connect dnf-refresh --cycle`, kept here so they are tested without the command.
//
// It holds the cycle and invocation rules, the checks of the service's answer, the outcome of each
// refresh path and the handoff document the appliance helper reads, with its custody-checked writer.
// It opens no connection and reads no connected-client state: cmd/olivares sends the operation and
// keeps its pending step.
//
// The download credential keeps the schema and the MAC key of the APT credential. The audience, the
// answer field and the handoff schema are the DNF channel's. The reason grammar is unchanged.
package dnfrefresh

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/license/connectv1"
)

// HandoffDir is the one directory dnf-refresh writes into. It is compiled into the product: no flag,
// environment variable or configuration names another, so no caller chooses a path.
const HandoffDir = "/run/olivares-dnf-handoff"

// CodeCycleInvalid is the product's refusal of a --cycle that is not 32 lowercase hex characters.
const CodeCycleInvalid = "cycle_invalid"

// ErrCycleInvalid is the refusal of a cycle; its text leads with CodeCycleInvalid.
var ErrCycleInvalid = errors.New(CodeCycleInvalid + ": --cycle must be exactly 32 lowercase hexadecimal characters")

// id128Pattern is a cycle or a systemd invocation id: 128 bits as 32 lowercase hex characters.
var id128Pattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// CheckCycle refuses anything but exactly 32 characters of [0-9a-f]. The command checks it before any
// network call, state read, operation change or file write.
func CheckCycle(cycle string) error {
	if !id128Pattern.MatchString(cycle) {
		return ErrCycleInvalid
	}
	return nil
}

// Invocation is the handoff's invocation: $INVOCATION_ID when it is exactly 32 lowercase hex
// characters, otherwise nil (null). The helper refuses a null or unequal invocation, so a run by hand
// never produces a handoff it accepts.
func Invocation(getenv func(string) string) *string {
	id := getenv("INVOCATION_ID")
	if !id128Pattern.MatchString(id) {
		return nil
	}
	return &id
}

// Outcome is the handoff's outcome of one dnf-refresh run.
type Outcome string

// The outcomes, the same set as apt-refresh.
const (
	OutcomeIssued                Outcome = "issued"
	OutcomeNotBound              Outcome = "not_bound"
	OutcomePendingOtherOperation Outcome = "pending_other_operation"
	OutcomeRefused               Outcome = "refused"
	OutcomeUnknown               Outcome = "unknown"
	OutcomeUnavailable           Outcome = "unavailable"
	OutcomeIdentityMismatch      Outcome = "identity_mismatch"
	OutcomeBusy                  Outcome = "busy"
)

var outcomes = map[Outcome]bool{
	OutcomeIssued: true, OutcomeNotBound: true, OutcomePendingOtherOperation: true, OutcomeRefused: true,
	OutcomeUnknown: true, OutcomeUnavailable: true, OutcomeIdentityMismatch: true, OutcomeBusy: true,
}

// Refusal is a connect-v1 refusal as the client read it: the HTTP status, the refusal code and the
// reason token the body names beside it ("" for none, see connectv1.RefusalReason).
type Refusal interface {
	error
	RefusalStatus() int
	RefusalCode() connectv1.ErrorCode
	RefusalReason() string
}

// r7Reasons are the reasons with which authority_denied is an authenticated definitive refusal.
// The helper de-admits on these reasons, so no other reason reaches that code.
var r7Reasons = map[string]bool{"revoked": true, "refunded": true, "absent": true}

// OutcomeOf is the outcome and code of an attempt that issued nothing. The code is "" when there is none.
//
// A refusal with a 4xx other than 401 is refused, and the command ends the pending operation exactly for
// refused; a 401 is unknown, because a proof refusal is not definitive; a 5xx is unavailable.
// authority_denied is refused only with reason revoked, refunded or absent; with any other reason or
// none it is unknown with no code, so the operation is kept. Without a refusal, an error that shows the
// service was not reached, or that time ran out, is unavailable, and anything else is unknown.
func OutcomeOf(err error) (Outcome, string) {
	var ref Refusal
	if errors.As(err, &ref) {
		code := string(ref.RefusalCode().Display())
		switch status := ref.RefusalStatus(); {
		case status == http.StatusUnauthorized:
			return OutcomeUnknown, code
		case status >= 400 && status < 500:
			if code == string(connectv1.ErrAuthorityDenied) && !r7Reasons[ref.RefusalReason()] {
				return OutcomeUnknown, ""
			}
			return OutcomeRefused, code
		case status >= 500:
			return OutcomeUnavailable, code
		}
		return OutcomeUnknown, code
	}
	if notReached(err) {
		return OutcomeUnavailable, ""
	}
	return OutcomeUnknown, ""
}

func notReached(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// CredentialPrefix starts every download credential. A download credential is not a licence.
const CredentialPrefix = "oad1."

// credentialSchema is the claim s of a download credential. The DNF channel does not rename it.
const credentialSchema = "olivares.ai/apt-download/v1"

// CredentialAudience is the claim aud of a DNF download credential: the DNF adapter's URI root,
// compared as this literal, never derived from the bound origin.
const CredentialAudience = "https://licenses.olivares.ai/dnf/v1/"

// maxReasonBytes bounds the answer's reason as the Worker's claim grammar bounds rsn (200 bytes).
const maxReasonBytes = 200

// maxLifetimeSeconds bounds exp − now: the credential's 86400 s plus 300 s of clock skew.
const maxLifetimeSeconds = 86700

// ErrAnswer is a dnf-refresh answer that failed the product's checks. Its outcome is unknown: the
// pending operation stays and the next run repeats it. Its text never carries an answer value.
var ErrAnswer = errors.New("dnfrefresh: the dnf-refresh answer failed its checks")

// setPattern is a canonical set: lowercase codes joined by +, as biz+ids+reg.
var setPattern = regexp.MustCompile(`^[a-z]+(\+[a-z]+)*$`)

// validCredential is the grammar ^oad1\.[A-Za-z0-9_-]{16,1024}\.[A-Za-z0-9_-]{43}$, at most 1073 bytes.
func validCredential(s string) bool {
	if len(s) > maxCredentialBytes {
		return false
	}
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

// answerFields are exactly the fields of the service's answer. The credential field is dnf_credential.
var answerFields = []string{"deployment_id", "binding_epoch", "class", "set", "suites", "context", "dnf_credential", "exp", "reason"}

// claimFields are exactly the claims of a download credential.
var claimFields = []string{"s", "aud", "cls", "h", "d", "k", "ep", "lin", "set", "su", "ctx", "rsn", "iat", "exp", "jti"}

// classSuites are the suites each class receives.
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

// CheckAnswer checks a strict-parsed dnf-refresh answer against the local binding at now, before any
// handoff is written: exactly the answer's fields; deployment_id and binding_epoch equal the binding;
// the credential's grammar, at most 1073 bytes; its decoded claims exactly the credential's, with aud
// equal to CredentialAudience, d, k and ep equal to the local binding and ctx equal to context; and
// exp − now ≤ 86700 s. A failure is ErrAnswer, and the pending operation stays.
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
	if a.Credential, err = connectv1.String(obj, "dnf_credential", maxCredentialBytes); err != nil || !validCredential(a.Credential) {
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

func checkClaims(claims map[string]any, b Binding, a Answer, reason string) error {
	str := func(k string) string { s, _ := claims[k].(string); return s }
	num := func(k string) (int64, bool) { n, ok := claims[k].(int64); return n, ok }
	switch {
	case str("s") != credentialSchema:
		return errors.New("the credential is not a download credential")
	case str("aud") != CredentialAudience:
		return errors.New("the credential's audience is not the DNF adapter's root")
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

// Supersedes reports whether a pending download refresh is replaced by next.
// A pending apt-refresh or dnf-refresh is replaced by any other connect operation,
// including the other download refresh. A repeat of the same operation is not
// replaced: the client returns the stored result. A pending bind, licence refresh,
// rotate, recover, reactivate or delete is not a download refresh, so it is not
// replaced and the download refresh answers pending_other_operation.
func Supersedes(pending, next string) bool {
	if pending == "" || pending == next {
		return false
	}
	return pending == connectv1.OpAptRefresh || pending == connectv1.OpDnfRefresh
}

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

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	cedar "github.com/cedar-policy/cedar-go"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// CD2 — the SCOPED grant engine must SIGNAL an SDK evaluation error without DISCLOSING
// it, on BOTH of its interfaces: auth.ScopedAuthorizer (Scoped) and auth.PolicyEvaluator
// (the restrict-view Evaluate). Both call logDiagErrors after real Cedar evaluation, and
// a Cedar diagnostic quotes the operator's own policy text together with the offending
// entity, so the warning may carry only a fixed product message, the fixed reason code
// and the integer error count.
//
// Two things make these assertions mean something rather than restate the code:
//
//  1. every diagnostic is a REAL one. The tests drive real compiled policies through the
//     real engine methods and then re-derive what Cedar actually reported by calling the
//     SAME production helpers those methods call (cd2ScopedSDKDiagnostic,
//     cd2RestrictViewSDKDiagnostic). An "it does not contain X" over a diagnostic that
//     was never generated proves nothing;
//  2. the WARN is separated from the INFO audit trail. logEffect legitimately records
//     tenant, principal and permission for an authorization DECISION — that is what the
//     audit trail is for. Containment is asserted over the diagnostic WARN record alone,
//     and the surviving INFO records are asserted to still carry their identity fields.
//     Passing this file by silencing the audit trail is therefore a failure, not a fix.

// Sentinels. Every value a caller controls on the request carries one, and the attribute
// name below exists ONLY in the operator's policy source, so any of them appearing in the
// diagnostic warning is a leak with a name rather than a judgement call.
const (
	cd2SentinelAttr        = "cd2_sentinel_attribute"
	cd2SentinelCred        = "cd2-sentinel-cred"
	cd2SentinelTenant      = "cd2-sentinel-tenant"
	cd2SentinelKind        = "cd2-sentinel-kind"
	cd2SentinelSensitivity = "cd2-sentinel-sensitivity"
	cd2SentinelExtraAttr   = "cd2_sentinel_extra"
	cd2SentinelExtraValue  = "cd2-sentinel-extra-value"
	cd2SentinelPermission  = "agent:write"
	cd2PolicyFilename      = "governance.grants.cedar"
)

// The fixtures below are REAL authored Cedar policies that compile and then fail at
// EVALUATION, one on each entity the scoped engine builds. The forbid reads an attribute
// the engine never populates on the PRINCIPAL entity, so its SDK diagnostic quotes the
// request's credential id; the permit does the same on the RESOURCE entity. The clean
// pair evaluate without error and are the silence controls.
const (
	cd2ForbidErrsOnPrincipal = `forbid(principal, action, resource) when { principal.cd2_sentinel_attribute == "x" };`
	cd2PermitErrsOnResource  = `permit(principal, action, resource) when { resource.cd2_sentinel_attribute == "x" };`
	cd2CleanPermit           = `permit(principal, action, resource);`
	cd2CleanForbid           = `forbid(principal, action, resource);`
)

// cd2WarningMessage is the fixed product message. It is written out here, not read from
// the package, so that changing the message is a test failure rather than a silent
// redefinition of what "fixed" means.
const cd2WarningMessage = "cedar scoped-grant evaluation error (guard attribute access with `has`)"

// cd2Clock pins the engine's time source so the control diagnostic below is derived from
// byte-identical Cedar context to the one the engine evaluated.
var cd2Clock = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

// cd2Request is the authorization question every CD2 test asks. It deliberately takes
// scopeResolver.resolve's documented no-store fast path (a collection-level action with
// no declared workspace), so the scope tree is not what is under test here and every
// mapped value stays a sentinel this file controls.
func cd2Request() auth.Request {
	return auth.Request{
		Principal:  auth.Principal{Kind: auth.KindUser, CredID: cd2SentinelCred},
		Permission: cd2SentinelPermission,
		Tenant:     model.TenantID(cd2SentinelTenant),
		Resource: auth.ResourceAttrs{
			Kind:        cd2SentinelKind,
			Sensitivity: cd2SentinelSensitivity,
			Extra:       map[string]string{cd2SentinelExtraAttr: cd2SentinelExtraValue},
		},
	}
}

// cd2NoStoreView is the resolver's read seam for exactly those fast-path requests. It
// never opens a transaction; if a change ever routed one of these requests through the
// store instead, the engine fails closed with this message rather than quietly resolving
// a scope tree the fixture never seeded.
type cd2NoStoreView struct{}

func (cd2NoStoreView) View(context.Context, model.TenantID, func(store.Scope) error) error {
	return errors.New("cd2: these fixtures take resolve's no-store fast path; no store read is expected")
}

// cd2Recorder is an in-memory slog sink that keeps each record's raw bytes next to its
// decoded fields. The timestamp is dropped so a record can be compared WHOLE — comparing
// the entire decoded record, not a subset of its keys, is what proves nothing else rides
// along — while the raw line is what the sentinel scan reads.
type cd2Recorder struct {
	buf    bytes.Buffer
	logger *slog.Logger
}

func newCD2Recorder() *cd2Recorder {
	r := &cd2Recorder{}
	r.logger = slog.New(slog.NewJSONHandler(&r.buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	return r
}

func (r *cd2Recorder) text() string { return r.buf.String() }

// cd2Record is one emitted record: its raw JSON line and its decoded fields.
type cd2Record struct {
	line   string
	fields map[string]any
}

func (r *cd2Recorder) records(t *testing.T) []cd2Record {
	t.Helper()
	var out []cd2Record
	for _, line := range strings.Split(strings.TrimSpace(r.buf.String()), "\n") {
		if line == "" {
			continue
		}
		rec := cd2Record{line: line, fields: map[string]any{}}
		if err := json.Unmarshal([]byte(line), &rec.fields); err != nil {
			t.Fatalf("decode log record %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// atLevel selects the records of one level, which is how the diagnostic WARN is kept
// apart from the INFO authorization audit trail that shares this logger.
func (r *cd2Recorder) atLevel(t *testing.T, level string) []cd2Record {
	t.Helper()
	var out []cd2Record
	for _, rec := range r.records(t) {
		if rec.fields["level"] == level {
			out = append(out, rec)
		}
	}
	return out
}

// cd2WantWarning is the ENTIRE record CD2 permits, decoded: the WARN level, the fixed
// product message, the fixed reason code and the integer count of SDK evaluation errors.
func cd2WantWarning(errorCount int) map[string]any {
	return map[string]any{
		"level":  "WARN",
		"msg":    cd2WarningMessage,
		"reason": "cedar_scoped_evaluation_error",
		"errors": float64(errorCount),
	}
}

// cd2AssertOneBoundedWarning checks the call emitted exactly one diagnostic record, that
// the record is exactly the permitted one, and that its raw bytes carry no sentinel. It
// asserts nothing about the INFO audit records, which each test checks explicitly.
func cd2AssertOneBoundedWarning(t *testing.T, r *cd2Recorder, errorCount int) cd2Record {
	t.Helper()
	warns := r.atLevel(t, "WARN")
	if len(warns) != 1 {
		t.Fatalf("CD2 allows at most one diagnostic warning per call, got %d record(s): %s", len(warns), r.text())
	}
	if want := cd2WantWarning(errorCount); !reflect.DeepEqual(warns[0].fields, want) {
		t.Errorf("contained warning mismatch\n got: %#v\nwant: %#v\nraw: %s", warns[0].fields, want, warns[0].line)
	}
	cd2AssertNoSentinels(t, warns[0].line)
	return warns[0]
}

// cd2AssertNoSentinels fails on any mapped question value, operator attribute name,
// policy source fragment or SDK policy identifier reaching the diagnostic record.
func cd2AssertNoSentinels(t *testing.T, logged string, extra ...string) {
	t.Helper()
	banned := []string{
		cd2SentinelAttr, cd2SentinelCred, cd2SentinelTenant, cd2SentinelKind,
		cd2SentinelSensitivity, cd2SentinelExtraAttr, cd2SentinelExtraValue,
		cd2SentinelPermission, cd2PolicyFilename, "forbid(", "permit(",
	}
	for _, b := range append(banned, extra...) {
		if b == "" {
			continue
		}
		if strings.Contains(logged, b) {
			t.Errorf("CD2: the diagnostic warning disclosed %q; record: %s", b, logged)
		}
	}
}

// cd2AssertNoWarning is the silence control: an error-free evaluation emits no diagnostic
// record at all, whatever else the audit trail recorded.
func cd2AssertNoWarning(t *testing.T, r *cd2Recorder) {
	t.Helper()
	if warns := r.atLevel(t, "WARN"); len(warns) != 0 {
		t.Errorf("an error-free evaluation must emit no diagnostic warning, got %d: %s", len(warns), r.text())
	}
}

// cd2AssertAuditEvent checks that the separate INFO authorization audit event survived
// with its identity fields intact. It is the other half of the containment claim: the
// diagnostic channel narrowed, the audit trail did not.
func cd2AssertAuditEvent(t *testing.T, r *cd2Recorder, msg string) {
	t.Helper()
	infos := r.atLevel(t, "INFO")
	if len(infos) != 1 {
		t.Fatalf("expected exactly one INFO audit event, got %d: %s", len(infos), r.text())
	}
	want := map[string]any{
		"level":          "INFO",
		"msg":            msg,
		"tenant":         cd2SentinelTenant,
		"principal_kind": string(auth.KindUser),
		"cred_id":        cd2SentinelCred,
		"permission":     cd2SentinelPermission,
		"resource_kind":  cd2SentinelKind,
		"resource_id":    "",
	}
	if !reflect.DeepEqual(infos[0].fields, want) {
		t.Errorf("the authorization audit event must be unchanged\n got: %#v\nwant: %#v", infos[0].fields, want)
	}
}

// cd2AssertNoAuditEvent pins the paths that record no authorization decision (an abstain,
// and the restrict-view's non-fail-closed outcomes), so this file cannot pass by having
// quietly added an audit event either.
func cd2AssertNoAuditEvent(t *testing.T, r *cd2Recorder) {
	t.Helper()
	if infos := r.atLevel(t, "INFO"); len(infos) != 0 {
		t.Errorf("this path records no authorization decision, got %d INFO record(s): %s", len(infos), r.text())
	}
}

// cd2Engine installs source as the tenant's live scoped policy through the existing
// cedarEpochState fixture and the production installIfNotOlder swap, and returns the real
// engine both interfaces are served from. A nil logger is passed through unchanged.
func cd2Engine(t *testing.T, source string, log *slog.Logger) *scopedEngine {
	t.Helper()
	e := &scopedEngine{
		resolver: &scopeResolver{data: cd2NoStoreView{}},
		now:      func() time.Time { return cd2Clock },
		log:      log,
	}
	state := cedarEpochState(model.TenantID(cd2SentinelTenant), 1, activationID{authored: 1}, source)
	if state.set == nil {
		t.Fatalf("fixture policy did not compile: %q", source)
	}
	if _, err := e.installIfNotOlder(model.TenantID(cd2SentinelTenant), state); err != nil {
		t.Fatalf("install scoped grant state: %v", err)
	}
	return e
}

// cd2Recorded builds the engine over an in-memory sink and returns both.
func cd2Recorded(t *testing.T, source string) (*scopedEngine, *cd2Recorder) {
	t.Helper()
	r := newCD2Recorder()
	return cd2Engine(t, source, r.logger), r
}

// cd2ScopedSDKDiagnostic re-derives what Cedar really reported for a Scoped call by
// running the engine's OWN compiled set through the SAME production helpers Scoped uses —
// resolve, actionUID, scopedContext and cedar.Authorize, in that order, on the engine's
// pinned clock. It is a control, not a second implementation: the tests assert its error
// count against the count the product warning published, so any drift surfaces as a
// mismatch instead of passing quietly.
func cd2ScopedSDKDiagnostic(t *testing.T, e *scopedEngine, req auth.Request) cedar.Diagnostic {
	t.Helper()
	state, loaded := e.tenantState(req.Tenant)
	if !loaded || state.set == nil {
		t.Fatal("control failed: the fixture installed no compiled scoped set")
	}
	em, resUID, pUID, err := e.resolver.resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("control failed: resolve: %v", err)
	}
	creq := cedar.Request{Principal: pUID, Action: actionUID(req), Resource: resUID, Context: scopedContext(req, e.clock())}
	_, diag := cedar.Authorize(state.set.policies, em, creq)
	return diag
}

// cd2RestrictViewSDKDiagnostic does the same for the restrict-view by calling the
// production evalGrantBasic, which is the exact store-free core Evaluate runs.
func cd2RestrictViewSDKDiagnostic(t *testing.T, e *scopedEngine, req auth.Request) cedar.Diagnostic {
	t.Helper()
	state, loaded := e.tenantState(req.Tenant)
	if !loaded || state.set == nil {
		t.Fatal("control failed: the fixture installed no compiled scoped set")
	}
	_, diag := evalGrantBasic(state.set, req, e.clock())
	return diag
}

// cd2AssertDiagnosticLeaks is the positive half of every omission assertion: it proves the
// diagnostic Cedar really produced carries the named sentinels and identifies a policy,
// and returns its first error so the negative half can ban that exact text.
func cd2AssertDiagnosticLeaks(t *testing.T, diag cedar.Diagnostic, wantErrors int, sentinels ...string) cedar.DiagnosticError {
	t.Helper()
	if len(diag.Errors) != wantErrors {
		t.Fatalf("control failed: expected %d SDK evaluation error(s), got %d", wantErrors, len(diag.Errors))
	}
	first := diag.Errors[0]
	for _, s := range sentinels {
		if !strings.Contains(first.Message, s) {
			t.Fatalf("control failed: expected the SDK message to quote %q, got %q", s, first.Message)
		}
	}
	if first.PolicyID == "" {
		t.Fatal("control failed: expected the SDK diagnostic to identify a policy")
	}
	return first
}

// --- interface 1: auth.ScopedAuthorizer (Scoped) ---------------------------------

// An errored FORBID keeps its F-06 fail-closed effect — with the invariant provenance
// that keeps it non-shadowable — reports the failure in one bounded warning, and still
// writes its authorization audit event.
func TestScopedDiagnosticErroredForbidWarnsBoundedAndStillFailsClosed(t *testing.T) {
	e, r := cd2Recorded(t, cd2ForbidErrsOnPrincipal)
	sd, err := e.Scoped(context.Background(), cd2Request())
	if err != nil {
		t.Fatalf("Scoped: %v", err)
	}
	if sd.Effect != auth.EffectForbid {
		t.Errorf("F-06: an errored forbid must still forbid, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd.Reason != "cedar: forbid rule evaluation error (fail-closed)" {
		t.Errorf("the fail-closed reason must not change, got %q", sd.Reason)
	}
	if sd.Class != auth.ClassInvariant {
		t.Errorf("an errored forbid must stay ClassInvariant (non-shadowable), got %d", sd.Class)
	}
	cd2AssertOneBoundedWarning(t, r, 1)
	cd2AssertAuditEvent(t, r, "cedar scoped forbid-error")
}

// The negative control for Scoped: the diagnostic this fixture really produces carries the
// operator's attribute name AND the request's credential id — precisely what used to be
// forwarded — and none of it reaches the warning, while the OCCURRENCE and the real error
// count still do. The audit event on the same call still carries that credential id, which
// is what makes this an assertion about the diagnostic channel and not about the logger.
func TestScopedDiagnosticWarningOmitsRealSDKDiagnosticContent(t *testing.T) {
	req := cd2Request()
	e, r := cd2Recorded(t, cd2ForbidErrsOnPrincipal)
	if _, err := e.Scoped(context.Background(), req); err != nil {
		t.Fatalf("Scoped: %v", err)
	}

	diag := cd2ScopedSDKDiagnostic(t, e, req)
	first := cd2AssertDiagnosticLeaks(t, diag, 1, cd2SentinelAttr, cd2SentinelCred)

	warns := r.atLevel(t, "WARN")
	if len(warns) != 1 {
		t.Fatalf("CD2 allows at most one diagnostic warning per call, got %d record(s): %s", len(warns), r.text())
	}
	// Not hidden: the error is reported, with a code an operator can alert on and the real
	// number of SDK errors behind it.
	if warns[0].fields["reason"] != "cedar_scoped_evaluation_error" {
		t.Errorf("the warning must carry the fixed reason code, got %v", warns[0].fields["reason"])
	}
	if got, want := warns[0].fields["errors"], float64(len(diag.Errors)); got != want {
		t.Errorf("the warning must report the real SDK error count %v, got %v", want, got)
	}
	// Not disclosed: neither the diagnostic text, nor the policy it names, nor its source.
	cd2AssertNoSentinels(t, warns[0].line, first.Message, string(first.PolicyID), first.Position.Filename, cd2ForbidErrsOnPrincipal)

	// The other half of the claim: the audit event is intact and does carry the identity
	// the warning omitted, so containment was not achieved by muting the audit trail.
	cd2AssertAuditEvent(t, r, "cedar scoped forbid-error")
	if infos := r.atLevel(t, "INFO"); !strings.Contains(infos[0].line, cd2SentinelCred) {
		t.Errorf("the audit event must still record the principal it authorized: %s", infos[0].line)
	}
}

// An errored PERMIT could only ever have widened, so Cedar's drop stands: the scoped
// effect is exactly the abstain it was before, no authorization decision is recorded, and
// the failure is still reported once.
func TestScopedDiagnosticErroredPermitKeepsOutcomeAndStillWarns(t *testing.T) {
	req := cd2Request()
	e, r := cd2Recorded(t, cd2PermitErrsOnResource)
	sd, err := e.Scoped(context.Background(), req)
	if err != nil {
		t.Fatalf("Scoped: %v", err)
	}
	if sd.Effect != auth.EffectAbstain || sd.Reason != "cedar: no grant matched" {
		t.Errorf("an errored permit must leave the RBAC decision standing, got %v (%s)", sd.Effect, sd.Reason)
	}
	// Control: this diagnostic quotes the operator's attribute name off a different entity
	// than the forbid case, and it too must not appear.
	first := cd2AssertDiagnosticLeaks(t, cd2ScopedSDKDiagnostic(t, e, req), 1, cd2SentinelAttr)
	warn := cd2AssertOneBoundedWarning(t, r, 1)
	cd2AssertNoSentinels(t, warn.line, first.Message, string(first.PolicyID), first.Position.Filename, cd2PermitErrsOnResource)
	cd2AssertNoAuditEvent(t, r)
}

// The count is the TOTAL of SDK evaluation errors and it travels in ONE warning: two
// broken policies produce two errors and a single record, and the errored forbid among
// them still fails closed.
func TestScopedDiagnosticCountsEverySDKErrorInOneRecord(t *testing.T) {
	req := cd2Request()
	e, r := cd2Recorded(t, cd2ForbidErrsOnPrincipal+"\n"+cd2PermitErrsOnResource+"\n")
	sd, err := e.Scoped(context.Background(), req)
	if err != nil {
		t.Fatalf("Scoped: %v", err)
	}
	if sd.Effect != auth.EffectForbid || sd.Reason != "cedar: forbid rule evaluation error (fail-closed)" {
		t.Errorf("the errored forbid must still fail closed with a second broken policy present, got %v (%s)", sd.Effect, sd.Reason)
	}
	if diag := cd2ScopedSDKDiagnostic(t, e, req); len(diag.Errors) != 2 {
		t.Fatalf("control failed: expected two SDK evaluation errors, got %d", len(diag.Errors))
	}
	cd2AssertOneBoundedWarning(t, r, 2)
	cd2AssertAuditEvent(t, r, "cedar scoped forbid-error")
}

// An error-free evaluation stays silent on the diagnostic channel while still recording
// the decision it made: a clean permit GRANTS and a clean forbid FORBIDS, and neither is
// an evaluation error.
func TestScopedDiagnosticSuccessfulEvaluationStaysSilentAndStillAudits(t *testing.T) {
	granted, gr := cd2Recorded(t, cd2CleanPermit)
	sd, err := granted.Scoped(context.Background(), cd2Request())
	if err != nil {
		t.Fatalf("Scoped (clean permit): %v", err)
	}
	if sd.Effect != auth.EffectGrant || sd.Reason != "cedar: scoped grant" {
		t.Errorf("a clean permit must GRANT, got %v (%s)", sd.Effect, sd.Reason)
	}
	cd2AssertNoWarning(t, gr)
	cd2AssertAuditEvent(t, gr, "cedar scoped grant")

	forbidden, fr := cd2Recorded(t, cd2CleanForbid)
	sd, err = forbidden.Scoped(context.Background(), cd2Request())
	if err != nil {
		t.Fatalf("Scoped (clean forbid): %v", err)
	}
	if sd.Effect != auth.EffectForbid || sd.Reason != "cedar: forbidden by policy" {
		t.Errorf("a clean forbid must FORBID, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd.Class != auth.ClassPolicy {
		t.Errorf("an authored forbid without evaluation errors remains ClassPolicy, got %d", sd.Class)
	}
	cd2AssertNoWarning(t, fr)
	cd2AssertAuditEvent(t, fr, "cedar scoped forbid")
}

// A nil logger stays valid on the erroring path: the warning is skipped and the
// fail-closed effect is unchanged.
func TestScopedDiagnosticNilLoggerStaysValidOnEvaluationError(t *testing.T) {
	e := cd2Engine(t, cd2ForbidErrsOnPrincipal, nil)
	sd, err := e.Scoped(context.Background(), cd2Request())
	if err != nil {
		t.Fatalf("Scoped: %v", err)
	}
	if sd.Effect != auth.EffectForbid || sd.Reason != "cedar: forbid rule evaluation error (fail-closed)" {
		t.Errorf("the nil-logger path must keep the fail-closed effect, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// --- interface 2: auth.PolicyEvaluator (the restrict-view Evaluate) ---------------

// The same errored forbid, through the hooks-PEP restrict-view: it still denies, still
// carries the invariant provenance, and emits one bounded warning. This path records no
// authorization decision, and that is unchanged too.
func TestRestrictViewDiagnosticErroredForbidWarnsBoundedAndStillFailsClosed(t *testing.T) {
	e, r := cd2Recorded(t, cd2ForbidErrsOnPrincipal)
	dec, err := e.Evaluate(context.Background(), cd2Request())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if dec.Allow {
		t.Errorf("F-06: an errored forbid must still deny in the restrict-view, got %+v", dec)
	}
	if dec.Reason != "cedar: forbidden by policy" {
		t.Errorf("the restrict-view reason must not change, got %q", dec.Reason)
	}
	if dec.Class != auth.ClassInvariant {
		t.Errorf("an errored forbid must stay ClassInvariant (non-shadowable), got %d", dec.Class)
	}
	cd2AssertOneBoundedWarning(t, r, 1)
	cd2AssertNoAuditEvent(t, r)
}

// The negative control for the restrict-view, derived from the production evalGrantBasic:
// the diagnostic really quotes the attribute name and the credential id, and the warning
// carries neither while still reporting the occurrence and the count.
func TestRestrictViewDiagnosticWarningOmitsRealSDKDiagnosticContent(t *testing.T) {
	req := cd2Request()
	e, r := cd2Recorded(t, cd2ForbidErrsOnPrincipal)
	if _, err := e.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	diag := cd2RestrictViewSDKDiagnostic(t, e, req)
	first := cd2AssertDiagnosticLeaks(t, diag, 1, cd2SentinelAttr, cd2SentinelCred)

	warns := r.atLevel(t, "WARN")
	if len(warns) != 1 {
		t.Fatalf("CD2 allows at most one diagnostic warning per call, got %d record(s): %s", len(warns), r.text())
	}
	if warns[0].fields["reason"] != "cedar_scoped_evaluation_error" {
		t.Errorf("the warning must carry the fixed reason code, got %v", warns[0].fields["reason"])
	}
	if got, want := warns[0].fields["errors"], float64(len(diag.Errors)); got != want {
		t.Errorf("the warning must report the real SDK error count %v, got %v", want, got)
	}
	cd2AssertNoSentinels(t, warns[0].line, first.Message, string(first.PolicyID), first.Position.Filename, cd2ForbidErrsOnPrincipal)
}

// An errored PERMIT keeps the restrict-view's prior result — it imposes no restriction —
// and the failure is still reported once.
func TestRestrictViewDiagnosticErroredPermitKeepsRestrictOnlyResultAndStillWarns(t *testing.T) {
	req := cd2Request()
	e, r := cd2Recorded(t, cd2PermitErrsOnResource)
	dec, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !dec.Allow || dec.Reason != "cedar: no restriction" {
		t.Errorf("an errored permit must impose no restriction, got %+v", dec)
	}
	first := cd2AssertDiagnosticLeaks(t, cd2RestrictViewSDKDiagnostic(t, e, req), 1, cd2SentinelAttr)
	warn := cd2AssertOneBoundedWarning(t, r, 1)
	cd2AssertNoSentinels(t, warn.line, first.Message, string(first.PolicyID), first.Position.Filename, cd2PermitErrsOnResource)
	cd2AssertNoAuditEvent(t, r)
}

// Two broken policies, one record, the real total — through the restrict-view.
func TestRestrictViewDiagnosticCountsEverySDKErrorInOneRecord(t *testing.T) {
	req := cd2Request()
	e, r := cd2Recorded(t, cd2ForbidErrsOnPrincipal+"\n"+cd2PermitErrsOnResource+"\n")
	dec, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if dec.Allow || dec.Class != auth.ClassInvariant {
		t.Errorf("the errored forbid must still deny as an invariant with a second broken policy present, got %+v", dec)
	}
	if diag := cd2RestrictViewSDKDiagnostic(t, e, req); len(diag.Errors) != 2 {
		t.Fatalf("control failed: expected two SDK evaluation errors, got %d", len(diag.Errors))
	}
	cd2AssertOneBoundedWarning(t, r, 2)
}

// A clean restrict-view evaluation is silent on the diagnostic channel: a clean permit
// imposes no restriction and a clean forbid restricts as authored business policy.
func TestRestrictViewDiagnosticSuccessfulEvaluationStaysSilent(t *testing.T) {
	permitted, pr := cd2Recorded(t, cd2CleanPermit)
	dec, err := permitted.Evaluate(context.Background(), cd2Request())
	if err != nil {
		t.Fatalf("Evaluate (clean permit): %v", err)
	}
	if !dec.Allow || dec.Reason != "cedar: no restriction" {
		t.Errorf("a clean permit must impose no restriction, got %+v", dec)
	}
	cd2AssertNoWarning(t, pr)

	restricted, rr := cd2Recorded(t, cd2CleanForbid)
	dec, err = restricted.Evaluate(context.Background(), cd2Request())
	if err != nil {
		t.Fatalf("Evaluate (clean forbid): %v", err)
	}
	if dec.Allow || dec.Class != auth.ClassPolicy {
		t.Errorf("a cleanly-evaluated authored forbid must restrict as ClassPolicy, got %+v", dec)
	}
	cd2AssertNoWarning(t, rr)

	if logged := pr.text() + rr.text(); logged != "" {
		t.Errorf("the restrict-view records no authorization decision, so a clean call emits nothing; logged: %s", logged)
	}
}

// A nil logger stays valid on the restrict-view erroring path too.
func TestRestrictViewDiagnosticNilLoggerStaysValidOnEvaluationError(t *testing.T) {
	e := cd2Engine(t, cd2ForbidErrsOnPrincipal, nil)
	dec, err := e.Evaluate(context.Background(), cd2Request())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if dec.Allow || dec.Class != auth.ClassInvariant {
		t.Errorf("the nil-logger path must keep the fail-closed decision, got %+v", dec)
	}
}

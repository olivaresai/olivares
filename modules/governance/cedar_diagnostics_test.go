// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	cedar "github.com/cedar-policy/cedar-go"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// CD1 — the Cedar PDP must SIGNAL an SDK evaluation error without DISCLOSING it. Cedar
// diagnostics quote the operator's own policy text and the offending entity, and the
// logger the caller injected is not a reviewed channel for either, so the warning may
// carry only a fixed product message, the fixed reason code and the integer error count.
//
// These tests drive the real interface — real compiled policies through
// NewCedarEvaluator and Evaluate — and check the emitted record against the SDK
// diagnostic the very same policy set really produces. That control is what makes the
// omission assertions mean anything: an "it does not contain X" over a diagnostic that
// was never generated proves nothing.

// Sentinels. Each appears in the mapped Cedar question, and the two attribute names
// appear in the operator's policy source, so any of them showing up in the log is a leak
// with a name rather than a judgement call.
const (
	cd1SentinelAttr        = "cd1_sentinel_attribute"
	cd1SentinelResourceID  = "cd1-sentinel-resource-id"
	cd1SentinelCred        = "cd1-sentinel-cred"
	cd1SentinelTenant      = "cd1-sentinel-tenant"
	cd1SentinelKind        = "cd1-sentinel-kind"
	cd1SentinelSensitivity = "cd1-sentinel-sensitivity"
	cd1SentinelExtraAttr   = "cd1_sentinel_extra"
	cd1SentinelExtraValue  = "cd1-sentinel-extra-value"
	cd1PolicyFilename      = "governance.cedar"
)

// The two fixtures below are REAL Cedar policies that compile and then fail at
// evaluation: the first reads an attribute the engine never populates on the resource
// entity, the second reads one off a principal entity that is not in the entity map.
// Their SDK diagnostics quote, respectively, the resource id and the credential id.
const (
	cd1ForbidErrsOnResource  = `forbid(principal, action, resource) when { resource.cd1_sentinel_attribute == "x" };`
	cd1PermitErrsOnPrincipal = `permit(principal, action, resource) when { principal.cd1_sentinel_attribute == "x" };`
)

// cd1WarningMessage is the fixed product message. It is written out here, not read from
// the package, so that changing the message is a test failure rather than a silent
// redefinition of what "fixed" means.
const cd1WarningMessage = "cedar policy evaluation error (guard attribute access with `has`)"

// cd1Request is the authorization question every CD1 test asks: every field a caller
// controls carries a sentinel, so the containment is checked against the whole mapped
// request rather than against the one value a given diagnostic happens to quote.
func cd1Request() auth.Request {
	return auth.Request{
		Principal:  auth.Principal{Kind: auth.KindUser, CredID: cd1SentinelCred},
		Permission: "agent:write",
		Tenant:     model.TenantID(cd1SentinelTenant),
		Resource: auth.ResourceAttrs{
			Kind:        cd1SentinelKind,
			ID:          cd1SentinelResourceID,
			Sensitivity: cd1SentinelSensitivity,
			Extra:       map[string]string{cd1SentinelExtraAttr: cd1SentinelExtraValue},
		},
	}
}

// cd1Recorder is an in-memory slog sink. The timestamp is dropped so a record can be
// compared WHOLE — comparing the entire decoded record, not a subset of its keys, is
// what proves nothing else rides along.
type cd1Recorder struct {
	buf    bytes.Buffer
	logger *slog.Logger
}

func newCD1Recorder() *cd1Recorder {
	r := &cd1Recorder{}
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

func (r *cd1Recorder) text() string { return r.buf.String() }

func (r *cd1Recorder) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(r.buf.String()), "\n") {
		if line == "" {
			continue
		}
		rec := map[string]any{}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode log record %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// cd1WantWarning is the ENTIRE record CD1 permits, decoded: a fixed level and message,
// the fixed reason code and the integer count of SDK evaluation errors.
func cd1WantWarning(errors int) map[string]any {
	return map[string]any{
		"level":  "WARN",
		"msg":    cd1WarningMessage,
		"reason": "cedar_evaluation_error",
		"errors": float64(errors),
	}
}

// cd1AssertOneBoundedWarning checks the call emitted exactly one record and that the
// record is exactly the permitted one, then re-checks the raw bytes for every sentinel.
func cd1AssertOneBoundedWarning(t *testing.T, r *cd1Recorder, errors int) {
	t.Helper()
	recs := r.records(t)
	if len(recs) != 1 {
		t.Fatalf("CD1 allows at most one warning per Evaluate call, got %d record(s): %s", len(recs), r.text())
	}
	if want := cd1WantWarning(errors); !reflect.DeepEqual(recs[0], want) {
		t.Errorf("contained warning mismatch\n got: %#v\nwant: %#v\nraw: %s", recs[0], want, r.text())
	}
	cd1AssertNoSentinels(t, r.text())
}

// cd1AssertNoSentinels fails on any request value, operator attribute name, policy
// source fragment or SDK policy identifier reaching the log.
func cd1AssertNoSentinels(t *testing.T, logged string, extra ...string) {
	t.Helper()
	banned := []string{
		cd1SentinelAttr, cd1SentinelResourceID, cd1SentinelCred, cd1SentinelTenant,
		cd1SentinelKind, cd1SentinelSensitivity, cd1SentinelExtraAttr, cd1SentinelExtraValue,
		cd1PolicyFilename, "agent:write", "forbid(", "permit(",
	}
	for _, b := range append(banned, extra...) {
		if b == "" {
			continue
		}
		if strings.Contains(logged, b) {
			t.Errorf("CD1: the warning disclosed %q; logged: %s", b, logged)
		}
	}
}

// cd1SDKDiagnostic runs the evaluator's OWN compiled policy set through the SDK with the
// same entity and request mapping Evaluate builds, and returns what Cedar really
// reported. It exists as a control, not as a second implementation: the tests assert its
// error count against the count the contained warning published, so a drift between this
// mapping and Evaluate's surfaces as a mismatch instead of passing quietly.
func cd1SDKDiagnostic(t *testing.T, ce *CedarEvaluator, req auth.Request) cedar.Diagnostic {
	t.Helper()
	resID := req.Resource.ID
	if resID == "" {
		resID = "*"
	}
	resUID := cedar.NewEntityUID(cedarTypeResource, cedar.String(resID))
	attrs := cedar.RecordMap{}
	for k, v := range req.Resource.Extra {
		attrs[cedar.String(k)] = cedar.String(v)
	}
	attrs["kind"] = cedar.String(req.Resource.Kind)
	attrs["sensitivity"] = cedar.String(req.Resource.Sensitivity)
	_, diag := cedar.Authorize(ce.policies,
		cedar.EntityMap{resUID: cedar.Entity{UID: resUID, Attributes: cedar.NewRecord(attrs)}},
		cedar.Request{
			Principal: cedar.NewEntityUID(cedarTypePrincipal, cedar.String(string(req.Principal.CredID))),
			Action:    cedar.NewEntityUID(cedarTypeAction, cedar.String(string(req.Permission))),
			Resource:  resUID,
			Context: cedar.NewRecord(cedar.RecordMap{
				"tenant":         cedar.String(string(req.Tenant)),
				"principal_kind": cedar.String(string(req.Principal.Kind)),
				"permission":     cedar.String(string(req.Permission)),
				"sensitivity":    cedar.String(req.Resource.Sensitivity),
				"time":           cedar.Long(ce.clock().Unix()),
			}),
		})
	return diag
}

// An errored FORBID keeps its F-06 fail-closed deny — with the invariant provenance that
// keeps it non-shadowable — and reports the failure in one bounded warning.
func TestCedarErroredForbidWarnsBoundedAndStillFailsClosed(t *testing.T) {
	r := newCD1Recorder()
	ce, err := NewCedarEvaluator(cd1ForbidErrsOnResource, r.logger)
	if err != nil {
		t.Fatalf("NewCedarEvaluator: %v", err)
	}
	dec, err := ce.Evaluate(context.Background(), cd1Request())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if dec.Allow {
		t.Errorf("F-06: an errored forbid must still deny, got %+v", dec)
	}
	if dec.Reason != "cedar: forbid rule evaluation error (fail-closed)" {
		t.Errorf("the fail-closed reason must not change, got %q", dec.Reason)
	}
	if dec.Class != auth.ClassInvariant {
		t.Errorf("an errored forbid must stay ClassInvariant (non-shadowable), got %d", dec.Class)
	}
	cd1AssertOneBoundedWarning(t, r, 1)
}

// The negative control: the SDK diagnostic this fixture really produces carries the
// operator's attribute name AND the request's resource identifier — precisely what used
// to be forwarded — and none of it reaches the logger, while the OCCURRENCE and the real
// error count still do.
func TestCedarBoundedWarningOmitsRealSDKDiagnosticContent(t *testing.T) {
	req := cd1Request()
	r := newCD1Recorder()
	ce, err := NewCedarEvaluator(cd1ForbidErrsOnResource, r.logger)
	if err != nil {
		t.Fatalf("NewCedarEvaluator: %v", err)
	}
	if _, err := ce.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	diag := cd1SDKDiagnostic(t, ce, req)
	if len(diag.Errors) == 0 {
		t.Fatal("control failed: this fixture produced no SDK evaluation error, so an omission assertion would prove nothing")
	}
	first := diag.Errors[0]
	if !strings.Contains(first.Message, cd1SentinelAttr) || !strings.Contains(first.Message, cd1SentinelResourceID) {
		t.Fatalf("control failed: expected the SDK message to quote the sentinel attribute and resource id, got %q", first.Message)
	}
	if first.PolicyID == "" {
		t.Fatal("control failed: expected the SDK diagnostic to identify a policy")
	}

	recs := r.records(t)
	if len(recs) != 1 {
		t.Fatalf("CD1 allows at most one warning per Evaluate call, got %d record(s): %s", len(recs), r.text())
	}
	// Not hidden: the error is reported, with a code an operator can alert on and the
	// real number of SDK errors behind it.
	if recs[0]["reason"] != "cedar_evaluation_error" {
		t.Errorf("the warning must carry the fixed reason code, got %v", recs[0]["reason"])
	}
	if got, want := recs[0]["errors"], float64(len(diag.Errors)); got != want {
		t.Errorf("the warning must report the real SDK error count %v, got %v", want, got)
	}
	// Not disclosed: neither the diagnostic text, nor the policy it names, nor its source.
	cd1AssertNoSentinels(t, r.text(), first.Message, string(first.PolicyID), first.Position.Filename, cd1ForbidErrsOnResource)
}

// An errored PERMIT could only ever have widened, so it stays dropped: the restrict-only
// result is exactly what it was before, and the failure is still reported.
func TestCedarErroredPermitKeepsRestrictOnlyResultAndStillWarns(t *testing.T) {
	req := cd1Request()
	r := newCD1Recorder()
	ce, err := NewCedarEvaluator(cd1PermitErrsOnPrincipal, r.logger)
	if err != nil {
		t.Fatalf("NewCedarEvaluator: %v", err)
	}
	dec, err := ce.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !dec.Allow || dec.Reason != "cedar: no forbid matched" {
		t.Errorf("an errored permit must leave the RBAC decision standing, got %+v", dec)
	}
	// Control: this diagnostic quotes the CREDENTIAL id, a different request value than
	// the forbid case leaks, and it too must not appear.
	diag := cd1SDKDiagnostic(t, ce, req)
	if len(diag.Errors) != 1 {
		t.Fatalf("control failed: expected exactly one SDK evaluation error, got %d", len(diag.Errors))
	}
	if !strings.Contains(diag.Errors[0].Message, cd1SentinelCred) {
		t.Fatalf("control failed: expected the SDK message to quote the sentinel credential id, got %q", diag.Errors[0].Message)
	}
	cd1AssertOneBoundedWarning(t, r, 1)
	cd1AssertNoSentinels(t, r.text(), diag.Errors[0].Message)
}

// The count is the TOTAL of SDK evaluation errors and it travels in ONE warning: two
// broken policies produce two errors and a single record.
func TestCedarWarningCountsEverySDKErrorInOneRecord(t *testing.T) {
	req := cd1Request()
	r := newCD1Recorder()
	ce, err := NewCedarEvaluator(cd1ForbidErrsOnResource+"\n"+cd1PermitErrsOnPrincipal+"\n", r.logger)
	if err != nil {
		t.Fatalf("NewCedarEvaluator: %v", err)
	}
	dec, err := ce.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if dec.Allow {
		t.Errorf("the errored forbid must still fail closed with a second broken policy present, got %+v", dec)
	}
	if diag := cd1SDKDiagnostic(t, ce, req); len(diag.Errors) != 2 {
		t.Fatalf("control failed: expected two SDK evaluation errors, got %d", len(diag.Errors))
	}
	cd1AssertOneBoundedWarning(t, r, 2)
}

// A successful evaluation stays silent: a matched forbid is a clean Deny and a
// non-matching one a clean Allow — neither is an evaluation error.
func TestCedarSuccessfulEvaluationStaysSilent(t *testing.T) {
	ctx := context.Background()
	r := newCD1Recorder()
	ce, err := NewCedarEvaluator(forbidSecret, r.logger)
	if err != nil {
		t.Fatalf("NewCedarEvaluator: %v", err)
	}
	if dec, _ := ce.Evaluate(ctx, cedarReq("secret")); dec.Allow {
		t.Error("forbid-on-secret must deny a secret resource")
	}
	if dec, _ := ce.Evaluate(ctx, cedarReq("public")); !dec.Allow {
		t.Error("forbid-on-secret must not restrict a public resource")
	}
	empty, err := NewCedarEvaluator("", r.logger)
	if err != nil {
		t.Fatalf("NewCedarEvaluator (empty): %v", err)
	}
	if dec, _ := empty.Evaluate(ctx, cd1Request()); !dec.Allow {
		t.Error("an empty policy set must impose no restriction")
	}
	if logged := r.text(); logged != "" {
		t.Errorf("a successful evaluation must emit nothing, logged: %s", logged)
	}
}

// A nil logger stays valid on the erroring path: the warning is skipped and the
// fail-closed decision is unchanged.
func TestCedarNilLoggerStaysValidOnEvaluationError(t *testing.T) {
	ce, err := NewCedarEvaluator(cd1ForbidErrsOnResource, nil)
	if err != nil {
		t.Fatalf("NewCedarEvaluator: %v", err)
	}
	dec, err := ce.Evaluate(context.Background(), cd1Request())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if dec.Allow || dec.Reason != "cedar: forbid rule evaluation error (fail-closed)" {
		t.Errorf("the nil-logger path must keep the fail-closed decision, got %+v", dec)
	}
}

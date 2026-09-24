// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"runtime"
	"slices"
	"time"

	"github.com/cedar-policy/cedar-go"
	cedarast "github.com/cedar-policy/cedar-go/ast"
	xast "github.com/cedar-policy/cedar-go/x/exp/ast"
)

// basePermit is always evaluated beside the measured policy. Its reason in the
// diagnostic proves that Authorize ran, whatever the measured policy decides.
const basePermit = "permit(principal, action, resource);"

// prepared is everything setup builds before the measured interval, and the
// operation's outputs, which are read only after every measurement.
type prepared struct {
	text     []byte
	pub      cedarast.Policy
	parsed   cedarast.Policy
	parseErr error
	base     *cedar.Policy
	compiled *cedar.Policy
	policies cedar.PolicyMap
	entities cedar.EntityMap
	req      cedar.Request
	decision cedar.Decision
	diag     cedar.Diagnostic
	nodes    int
	walk     walkResult
	ran      bool
	op       func()
}

// productRequest returns a request shaped like the product's: flat context of
// strings and longs, with a fixed time instead of the wall clock.
func productRequest(t int64) cedar.Request {
	return cedar.Request{
		Principal: cedar.NewEntityUID("User", "u"),
		Action:    cedar.NewEntityUID("Action", "read"),
		Resource:  cedar.NewEntityUID("Resource", "r"),
		Context: cedar.NewRecord(cedar.RecordMap{
			"tenant":         cedar.String("t"),
			"principal_kind": cedar.String("user"),
			"permission":     cedar.String("read"),
			"sensitivity":    cedar.String(""),
			"aal":            cedar.Long(1),
			"time":           cedar.Long(t),
		}),
	}
}

func compileText(text string) (*cedar.Policy, *failure) {
	var p cedarast.Policy
	if err := p.UnmarshalCedar([]byte(text)); err != nil {
		return nil, mismatch("base_parse")
	}
	return cedar.NewPolicyFromAST(&p), nil
}

// prepare generates and checks the input, then builds the operation. It runs
// in a goroutine that exits before the measurement starts.
func prepare(c child, requestTime int64) (*prepared, *failure) {
	text, err := generate(c.Cell, c.Point, requestTime)
	if err != nil {
		return nil, mismatch("generator")
	}
	sum := sha256.Sum256([]byte(text))
	if len(text) != c.InputBytes || hex.EncodeToString(sum[:]) != c.InputSHA256 {
		return nil, mismatch("input_digest")
	}
	pr := &prepared{text: []byte(text), entities: cedar.EntityMap{}, req: productRequest(requestTime)}
	if c.Op == opParse {
		pr.op = func() {
			pr.parseErr = pr.parsed.UnmarshalCedar(pr.text)
			pr.ran = true
		}
		return pr, nil
	}
	if err := pr.pub.UnmarshalCedar(pr.text); err != nil {
		return nil, mismatch("generator_parse")
	}
	if c.Op != opWalk {
		if w := walkPolicy((*xast.Policy)(&pr.pub)); w.Steps != c.Expected.Steps {
			return nil, mismatch("generator_shape")
		}
	}
	base, f := compileText(basePermit)
	if f != nil {
		return nil, f
	}
	pr.base = base
	switch c.Op {
	case opCompile:
		pr.op = func() {
			pr.compiled = cedar.NewPolicyFromAST(&pr.pub)
			pr.ran = true
		}
	case opAuthorize:
		pr.compiled = cedar.NewPolicyFromAST(&pr.pub)
		pr.policies = cedar.PolicyMap{"p0": pr.compiled, "base": pr.base}
		pr.op = func() {
			pr.decision, pr.diag = cedar.Authorize(pr.policies, pr.entities, pr.req)
			pr.ran = true
		}
	case opInspect:
		conditions := (*xast.Policy)(&pr.pub).Conditions
		if len(conditions) != 1 {
			return nil, mismatch("generator_shape")
		}
		body := conditions[0].Body
		pr.op = func() {
			n := 0
			xast.Inspect(xast.NewNode(body), func(xast.IsNode) bool {
				n++
				return true
			})
			pr.nodes = n
			pr.ran = true
		}
	case opWalk:
		pr.op = func() {
			pr.walk = walkPolicy((*xast.Policy)(&pr.pub))
			pr.ran = true
		}
	default:
		return nil, mismatch("unknown_op")
	}
	return pr, nil
}

func reasonIDs(d cedar.Diagnostic) []string {
	ids := make([]string, 0, len(d.Reasons))
	for _, r := range d.Reasons {
		ids = append(ids, string(r.PolicyID))
	}
	slices.Sort(ids)
	return ids
}

// check reads the operation's outputs after every measurement. Work it needs,
// such as an Authorize of a compiled policy, happens here, outside the
// measured interval, and never replaces the operation's own output.
func check(c child, pr *prepared, requestTime int64) (*result, *failure) {
	r := &result{Ran: pr.ran}
	if !pr.ran {
		return r, mismatch("operation_not_executed")
	}
	switch c.Op {
	case opParse:
		if pr.parseErr != nil {
			return r, mismatch("parse_refused")
		}
		xp := (*xast.Policy)(&pr.parsed)
		r.Steps = walkPolicy(xp).Steps
		r.Annotations = len(xp.Annotations)
		if eq, ok := xp.Principal.(xast.ScopeTypeEq); ok {
			r.TypeLen = len(string(eq.Entity.Type))
		}
	case opCompile:
		r.Compiled = pr.compiled != nil
		if !r.Compiled {
			return r, mismatch("compile_result_missing")
		}
		_, d := cedar.Authorize(cedar.PolicyMap{"p0": pr.compiled, "base": pr.base}, pr.entities, productRequest(requestTime))
		r.Reasons, r.Errors = reasonIDs(d), len(d.Errors)
	case opAuthorize:
		r.Reasons, r.Errors = reasonIDs(pr.diag), len(pr.diag.Errors)
		if c.Expected.Discriminates {
			_, d := cedar.Authorize(pr.policies, pr.entities, productRequest(requestTime+1))
			r.Discriminates = slices.Equal(reasonIDs(d), []string{"base"}) && len(d.Errors) == 0
		}
	case opInspect:
		r.Nodes = pr.nodes
	case opWalk:
		r.Steps, r.Depth = pr.walk.Steps, pr.walk.Depth
	}
	if reason := compare(c.Op, c.Expected, r); reason != "" {
		return r, mismatch(reason)
	}
	return r, nil
}

// compare returns the first result field that differs from the expectation.
func compare(op string, e expected, r *result) string {
	switch op {
	case opParse:
		if r.Steps != e.Steps {
			return "result_steps"
		}
		if e.TypeLen != 0 && r.TypeLen != e.TypeLen {
			return "result_type_len"
		}
		if e.Annotations != 0 && r.Annotations != e.Annotations {
			return "result_annotations"
		}
	case opCompile, opAuthorize:
		if !slices.Equal(r.Reasons, e.Reasons) {
			return "result_reasons"
		}
		if r.Errors != e.Errors {
			return "result_errors"
		}
		if e.Discriminates && !r.Discriminates {
			return "result_not_discriminating"
		}
	case opInspect:
		if r.Nodes != e.Nodes {
			return "result_nodes"
		}
	case opWalk:
		if r.Steps != e.Steps || r.Depth != e.Depth {
			return "result_walk"
		}
	}
	return ""
}

func runChild(args []string) int {
	start := time.Now()
	fs := flag.NewFlagSet("child", flag.ContinueOnError)
	matrixPath := fs.String("matrix", "", "path of MATRIX.json")
	id := fs.String("id", "", "child id, CELL/OP/POINT")
	token := fs.String("token", "", "token chosen by the supervisor for this child")
	control := fs.String("control", controlNone, "deliberate defect for the hosted control phase")
	rec := &record{Schema: recordSchema, Mode: "child", Control: controlNone, Env: currentEnv()}
	if err := fs.Parse(args); err != nil {
		return emit(rec, exitInability, "flags")
	}
	rec.Token, rec.ID, rec.Control = *token, *id, *control
	switch *control {
	case controlNone, controlSkipOp, controlReadBefore:
	default:
		return emit(rec, exitInability, "unknown_control")
	}
	m, err := loadMatrix(*matrixPath)
	if err != nil {
		return emit(rec, exitInability, "matrix_unreadable")
	}
	c, ok := m.find(*id)
	if !ok {
		return emit(rec, exitMismatch, "unknown_child")
	}
	rec.Cell, rec.Op, rec.Point = c.Cell, c.Op, c.Point
	p, err := newProbe(start)
	if err != nil {
		return emit(rec, exitInability, "metric_unsupported")
	}

	var pr *prepared
	var f *failure
	phase("setup")
	setupStart := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		pr, f = prepare(c, m.RequestTime)
	}()
	<-done
	rec.SetupNS = time.Since(setupStart).Nanoseconds()
	if f != nil {
		return emit(rec, f.code, f.reason)
	}
	rec.InputBytes = len(pr.text)
	sum := sha256.Sum256(pr.text)
	rec.InputSHA256 = hex.EncodeToString(sum[:])

	phase("measure")
	meas := measure(p, pr.op, *control, c.Op == opCompile)
	phase("check")
	rec.Measurement = &meas
	res, f := check(c, pr, m.RequestTime)
	rec.Result = res
	runtime.KeepAlive(pr)
	if f != nil {
		return emit(rec, f.code, f.reason)
	}
	return emit(rec, exitObserved, "")
}

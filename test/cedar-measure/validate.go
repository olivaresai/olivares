// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"slices"
	"strconv"

	"github.com/cedar-policy/cedar-go"
	cedarast "github.com/cedar-policy/cedar-go/ast"
	xast "github.com/cedar-policy/cedar-go/x/exp/ast"
)

// validation is the detail of a validate record.
type validation struct {
	ChildrenChecked  int      `json:"children_checked"`
	LowPointsChecked int      `json:"low_points_checked"`
	Failures         []string `json:"failures"`
}

// runValidate checks, before any measurement, that every matrix input is
// generated with its recorded length and digest, and that every low point
// reproduces its hand-written text and hand-derived results.
func runValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	matrixPath := fs.String("matrix", "", "path of MATRIX.json")
	token := fs.String("token", "", "token chosen by the supervisor")
	rec := &record{Schema: recordSchema, Mode: "validate", Control: controlNone, Env: currentEnv()}
	if err := fs.Parse(args); err != nil {
		return emit(rec, exitInability, "flags")
	}
	rec.Token = *token
	phase("validate")
	m, err := loadMatrix(*matrixPath)
	if err != nil {
		return emit(rec, exitInability, "matrix_unreadable")
	}
	v := validation{Failures: []string{}}
	for _, c := range m.Children {
		v.ChildrenChecked++
		text, err := generate(c.Cell, c.Point, m.RequestTime)
		sum := sha256.Sum256([]byte(text))
		if err != nil || len(text) != c.InputBytes || hex.EncodeToString(sum[:]) != c.InputSHA256 {
			v.Failures = append(v.Failures, "input:"+c.ID)
		}
	}
	for _, lp := range m.LowPoints {
		v.LowPointsChecked++
		if reason := checkLowPoint(lp, m.RequestTime); reason != "" {
			v.Failures = append(v.Failures, "low:"+lp.Cell+"/"+strconv.Itoa(lp.Point)+":"+reason)
		}
	}
	rec.Detail = v
	if len(v.Failures) > 0 {
		return emit(rec, exitMismatch, "validation_failed")
	}
	return emit(rec, exitObserved, "")
}

// checkLowPoint returns "" when the low point holds, or the first field that
// does not.
func checkLowPoint(lp lowPoint, requestTime int64) string {
	text, err := generate(lp.Cell, lp.Point, requestTime)
	if err != nil {
		return "generator"
	}
	if text != lp.Text {
		return "text"
	}
	var p cedarast.Policy
	if err := p.UnmarshalCedar([]byte(text)); err != nil {
		return "parse"
	}
	xp := (*xast.Policy)(&p)
	w := walkPolicy(xp)
	if w.Steps != lp.Expected.Steps {
		return "steps"
	}
	if lp.Expected.Depth != 0 && w.Depth != lp.Expected.Depth {
		return "depth"
	}
	if lp.Expected.TypeLen != 0 {
		eq, ok := xp.Principal.(xast.ScopeTypeEq)
		if !ok || len(string(eq.Entity.Type)) != lp.Expected.TypeLen {
			return "type_len"
		}
	}
	if lp.Expected.Annotations != 0 && len(xp.Annotations) != lp.Expected.Annotations {
		return "annotations"
	}
	if lp.Expected.Nodes != 0 {
		if len(xp.Conditions) != 1 {
			return "nodes"
		}
		n := 0
		xast.Inspect(xast.NewNode(xp.Conditions[0].Body), func(xast.IsNode) bool {
			n++
			return true
		})
		if n != lp.Expected.Nodes {
			return "nodes"
		}
	}
	if len(lp.Expected.Reasons) > 0 {
		base, f := compileText(basePermit)
		if f != nil {
			return "base"
		}
		policies := cedar.PolicyMap{"p0": cedar.NewPolicyFromAST(&p), "base": base}
		_, d := cedar.Authorize(policies, cedar.EntityMap{}, productRequest(requestTime))
		if !slices.Equal(reasonIDs(d), lp.Expected.Reasons) || len(d.Errors) != lp.Expected.Errors {
			return "decision"
		}
		if lp.Expected.Discriminates {
			_, d2 := cedar.Authorize(policies, cedar.EntityMap{}, productRequest(requestTime+1))
			if !slices.Equal(reasonIDs(d2), []string{"base"}) || len(d2.Errors) != 0 {
				return "discrimination"
			}
		}
	}
	return ""
}

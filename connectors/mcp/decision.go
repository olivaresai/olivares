// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode"

	cedar "github.com/cedar-policy/cedar-go"
	cedarast "github.com/cedar-policy/cedar-go/x/exp/ast"
)

// decision.go is the tools/call decision table. Its rows are read in
// first-match order:
//
//	kill switch  the estate or agent stop, owned by the composition: its
//	             ApprovalGate and upstream guard refuse before any approval
//	             or dispatch (cmd/olivares/mcpgateway.go)
//	block        no toolset entry, an entry with deny, or a matched Cedar forbid
//	ask          a destructive entry, or matched forbids that all carry
//	             @decision("ask"); a human approves through the ApprovalGate
//	allow        everything else
//
// Conditions only tighten: a destructive tool never becomes allow, and a block
// always wins over an ask.

// Decision is the outcome of one row of the tools/call decision table.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionAsk   Decision = "ask"
	DecisionBlock Decision = "block"
)

// toolVerdict is the table's answer for one admitted call. RuleID names the row
// for the audit trail: "tool:<name>" for the entry, or
// "tool:<name>/condition:<id>" for a Cedar rule.
type toolVerdict struct {
	Decision Decision
	RuleID   string
	Reason   string
}

// conditionDecisionAnnotation selects ask instead of block on a forbid rule. The
// rule ID comes from Cedar's own @id annotation when the operator sets one.
const (
	conditionDecisionAnnotation = "decision"
	conditionIDAnnotation       = "id"
)

// compileToolConditions parses one tool's Cedar conditions. Only forbid rules are
// accepted: a permit cannot widen anything here, and refusing it keeps an
// operator from reading one as an allow rule.
func compileToolConditions(tool, src string) (*cedar.PolicySet, error) {
	ps, err := cedar.NewPolicySetFromBytes("tool "+tool+" conditions", []byte(src))
	if err != nil {
		return nil, fmt.Errorf("tool %q conditions: %w", tool, err)
	}
	rules := 0
	for id, p := range ps.All() {
		rules++
		if p.Effect() != cedar.Forbid {
			return nil, fmt.Errorf("tool %q conditions: rule %s is not a forbid rule; conditions only restrict", tool, conditionRuleID(id, p))
		}
		if d, ok := p.Annotations()[conditionDecisionAnnotation]; ok && d != cedar.String(DecisionAsk) && d != cedar.String(DecisionBlock) {
			return nil, fmt.Errorf("tool %q conditions: rule %s has @decision(%q); accepted values are %q and %q",
				tool, conditionRuleID(id, p), string(d), DecisionAsk, DecisionBlock)
		}
		// The request carries no entity hierarchy, so an `in` scope could only
		// ever match its own entity: a group or parent never matches, and the
		// forbid would silently never apply.
		scope := (*cedarast.Policy)(p.AST())
		switch scope.Principal.(type) {
		case cedarast.ScopeTypeIn, cedarast.ScopeTypeIsIn:
			return nil, fmt.Errorf("tool %q conditions: rule %s scopes principal with `in`; conditions have no entity hierarchy, use ==", tool, conditionRuleID(id, p))
		}
		switch scope.Resource.(type) {
		case cedarast.ScopeTypeIn, cedarast.ScopeTypeIsIn:
			return nil, fmt.Errorf("tool %q conditions: rule %s scopes resource with `in`; conditions have no entity hierarchy, use ==", tool, conditionRuleID(id, p))
		}
	}
	if rules == 0 {
		return nil, fmt.Errorf("tool %q conditions: no rules", tool)
	}
	return ps, nil
}

func conditionRuleID(id cedar.PolicyID, p *cedar.Policy) string {
	if named, ok := p.Annotations()[conditionIDAnnotation]; ok && named != "" {
		return string(named)
	}
	return string(id)
}

// decide returns the row for an admitted call to a tool the toolset resolved.
// args are the canonical arguments the plan hash and the forwarded bytes bind.
func (t *Toolset) decide(tool string, policy ToolPolicy, subject string, args []byte) toolVerdict {
	verdict := toolVerdict{Decision: DecisionAllow, RuleID: "tool:" + tool, Reason: "tool allowed by the server toolset"}
	if policy.Destructive {
		verdict = toolVerdict{Decision: DecisionAsk, RuleID: "tool:" + tool, Reason: "destructive tool requires human approval"}
	}
	var conditions *cedar.PolicySet
	if t != nil {
		conditions = t.conditions[tool]
	}
	if conditions == nil {
		if strings.TrimSpace(policy.Conditions) != "" {
			// Conditions the toolset did not compile cannot be evaluated.
			return toolVerdict{Decision: DecisionBlock, RuleID: "tool:" + tool + "/condition:unavailable", Reason: "conditions are not compiled (deny-closed)"}
		}
		return verdict
	}
	conditional := evaluateToolConditions(conditions, tool, subject, args)
	if decisionRank(conditional.Decision) > decisionRank(verdict.Decision) {
		return conditional
	}
	return verdict
}

func decisionRank(d Decision) int {
	switch d {
	case DecisionAllow:
		return 0
	case DecisionAsk:
		return 1
	default:
		return 2 // block, and anything unknown
	}
}

// evaluateToolConditions runs one tool's forbid rules against the call:
// principal Principal::"<subject>", action Action::"tools/call", resource
// Resource::"<tool>", and context {tool, arguments}. A rule that cannot be
// evaluated blocks the call: Cedar skips an erroring rule, and a skipped forbid
// would fail open.
func evaluateToolConditions(ps *cedar.PolicySet, tool, subject string, args []byte) toolVerdict {
	block := func(rule, reason string) toolVerdict {
		return toolVerdict{Decision: DecisionBlock, RuleID: "tool:" + tool + "/condition:" + rule, Reason: reason}
	}
	arguments := cedar.NewRecord(cedar.RecordMap{})
	if len(args) > 0 {
		tree, err := decodeStrictJSON(args)
		if err != nil || tree.kind != canonObject {
			return block("arguments", "tool arguments are not an object the conditions can read (deny-closed)")
		}
		if arguments, err = cedarRecord(tree); err != nil {
			return block("arguments", "tool arguments cannot be read exactly by the conditions: "+err.Error()+" (deny-closed)")
		}
	}
	req := cedar.Request{
		Principal: cedar.NewEntityUID("Principal", cedar.String(subject)),
		Action:    cedar.NewEntityUID("Action", "tools/call"),
		Resource:  cedar.NewEntityUID("Resource", cedar.String(tool)),
		Context:   cedar.NewRecord(cedar.RecordMap{"tool": cedar.String(tool), "arguments": arguments}),
	}
	_, diag := cedar.Authorize(ps, nil, req)
	if len(diag.Errors) > 0 {
		ids := make([]string, 0, len(diag.Errors))
		for _, e := range diag.Errors {
			ids = append(ids, conditionRuleID(e.PolicyID, ps.Get(e.PolicyID)))
		}
		sort.Strings(ids)
		return block(ids[0], "condition could not be evaluated (deny-closed)")
	}
	if len(diag.Reasons) == 0 {
		return toolVerdict{Decision: DecisionAllow, RuleID: "tool:" + tool, Reason: "no condition matched"}
	}
	var asks, blocks []string
	for _, r := range diag.Reasons {
		p := ps.Get(r.PolicyID)
		if p.Annotations()[conditionDecisionAnnotation] == cedar.String(DecisionAsk) {
			asks = append(asks, conditionRuleID(r.PolicyID, p))
		} else {
			blocks = append(blocks, conditionRuleID(r.PolicyID, p))
		}
	}
	if len(blocks) > 0 {
		sort.Strings(blocks)
		return block(blocks[0], "a condition forbids these arguments")
	}
	sort.Strings(asks)
	return toolVerdict{Decision: DecisionAsk, RuleID: "tool:" + tool + "/condition:" + asks[0], Reason: "a condition requires human approval for these arguments"}
}

// cedarRecord maps a strictly decoded JSON object onto a Cedar record, so a
// condition reads the value the upstream acts on: objects become records, arrays
// sets, strings and booleans themselves, numbers longs or decimals. Cedar has no
// null: a null member is absent. Anything a condition could misread is refused:
//   - names that differ only by case, which a case-insensitive decoder (Go's
//     encoding/json, for one) reads as one name the condition did not test;
//   - a number Cedar cannot hold exactly.
func cedarRecord(v canonValue) (cedar.Record, error) {
	m := cedar.RecordMap{}
	folded := make(map[string]struct{}, len(v.obj))
	for _, member := range v.obj {
		key := foldKey(member.key)
		if _, dup := folded[key]; dup {
			return cedar.Record{}, errors.New("two argument names differ only by case")
		}
		folded[key] = struct{}{}
		value, present, err := cedarValue(member.val)
		if err != nil {
			return cedar.Record{}, err
		}
		if present {
			m[cedar.String(member.key)] = value
		}
	}
	return cedar.NewRecord(m), nil
}

func cedarValue(v canonValue) (cedar.Value, bool, error) {
	switch v.kind {
	case canonBool:
		return cedar.Boolean(v.b), true, nil
	case canonNumber:
		n, err := cedarNumber(v.num)
		return n, err == nil, err
	case canonString:
		return cedar.String(v.str), true, nil
	case canonArray:
		values := make([]cedar.Value, 0, len(v.arr))
		for _, e := range v.arr {
			value, present, err := cedarValue(e)
			if err != nil {
				return nil, false, err
			}
			if present {
				values = append(values, value)
			}
		}
		return cedar.NewSet(values...), true, nil
	case canonObject:
		r, err := cedarRecord(v)
		return r, err == nil, err
	default:
		return nil, false, nil
	}
}

// errArgumentNumber refuses a number Cedar cannot hold exactly.
var errArgumentNumber = errors.New("a number is outside Cedar's long and four-digit decimal")

// cedarNumber maps a JSON number exactly: an integral value (1, 1.0, 1e3) is a
// long, a value with at most four fractional digits a Cedar decimal. Literal
// length and exponent are bounded before any arithmetic, so a hostile exponent
// costs nothing.
func cedarNumber(n json.Number) (cedar.Value, error) {
	lit := n.String()
	if len(lit) > 64 {
		return nil, errArgumentNumber
	}
	if i := strings.IndexAny(lit, "eE"); i >= 0 {
		if exp, err := strconv.Atoi(lit[i+1:]); err != nil || exp > 32 || exp < -32 {
			return nil, errArgumentNumber
		}
	}
	r, ok := new(big.Rat).SetString(lit)
	if !ok {
		return nil, errArgumentNumber
	}
	if r.IsInt() {
		if !r.Num().IsInt64() {
			return nil, errArgumentNumber
		}
		return cedar.Long(r.Num().Int64()), nil
	}
	scaled := new(big.Rat).Mul(r, big.NewRat(10000, 1))
	if !scaled.IsInt() || !scaled.Num().IsInt64() {
		return nil, errArgumentNumber
	}
	d, err := cedar.NewDecimal(scaled.Num().Int64(), -4)
	if err != nil {
		return nil, errArgumentNumber
	}
	return d, nil
}

// foldKey is the name a case-insensitive decoder matches: every rune replaced by
// the smallest rune of its Unicode simple-fold orbit, the folding strings.EqualFold
// and encoding/json apply (so "ſ" folds with "s" and the Kelvin sign with "k").
func foldKey(s string) string {
	return strings.Map(func(r rune) rune {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < least {
				least = f
			}
		}
		return least
	}, s)
}

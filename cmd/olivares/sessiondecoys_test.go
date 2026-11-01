// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/inferencepep"
	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
)

// decoyOnlyEngine is an inspector that is the edition's decoy engine and nothing else.
type decoyOnlyEngine struct{ fixedInspector }

func (*decoyOnlyEngine) PlantSessionDecoys(context.Context, model.TenantID, string, string, string) (func(), error) {
	return nil, nil
}

type fixedInspector struct {
	dec   claudeapi.ContentInspectionDecision
	calls int
}

func (f *fixedInspector) Inspect(context.Context, claudeapi.ContentInspectionInput) claudeapi.ContentInspectionDecision {
	f.calls++
	return f.dec
}

// A Community build has no decoy engine: the chain at each site is the inspector it
// already had, nil included, so nothing there changes.
func TestChainWithNoEngineIsTheInspectorTheSiteHad(t *testing.T) {
	var none sessionDecoyEngine
	if chainInspectors(nil, none) != nil {
		t.Fatal("an absent inspector and an absent engine must chain to nil, not a typed nil")
	}
	one := &fixedInspector{}
	if got := chainInspectors(one, none); got != contentInspector(one) {
		t.Fatalf("one live inspector must be returned unchanged, got %T", got)
	}
}

func TestChainInspectorsMergesVerdicts(t *testing.T) {
	deny := claudeapi.ContentInspectionDecision{Status: 403, ErrorType: "permission_error", Reason: "first deny"}
	secondDeny := claudeapi.ContentInspectionDecision{Status: 451, ErrorType: "other", Reason: "second deny"}
	findA := claudeapi.ContentInspectionFinding{Kind: "a"}
	findB := claudeapi.ContentInspectionFinding{Kind: "b"}
	one := claudeapi.ContentInspectionMeter{Inspections: 1, Channels: 2, Bytes: 10, Detectors: 3}
	pass := func(fs ...claudeapi.ContentInspectionFinding) claudeapi.ContentInspectionDecision {
		return claudeapi.ContentInspectionDecision{Forward: true, Findings: fs, Meter: one}
	}
	cases := []struct {
		name        string
		in          []claudeapi.ContentInspectionDecision
		wantForward bool
		wantBlock   bool
		wantStatus  int
		wantReason  string
		wantKinds   []string
		wantMeter   claudeapi.ContentInspectionMeter
	}{
		{"both pass, findings and meters add", []claudeapi.ContentInspectionDecision{pass(findA), pass(findB)}, true, false, 0, "", []string{"a", "b"},
			claudeapi.ContentInspectionMeter{Inspections: 2, Channels: 4, Bytes: 20, Detectors: 6}},
		{"a deny from the second still denies", []claudeapi.ContentInspectionDecision{pass(findA), deny}, false, false, 403, "first deny", []string{"a"}, one},
		{"the first deny's status and reason are the verdict", []claudeapi.ContentInspectionDecision{deny, secondDeny}, false, false, 403, "first deny", nil, claudeapi.ContentInspectionMeter{}},
		{"a non-forward without a status is still a deny, as each consumer reads it alone", []claudeapi.ContentInspectionDecision{{}, pass(findB)}, false, false, 0, "", []string{"b"}, one},
		{"a block holds the response", []claudeapi.ContentInspectionDecision{pass(), {Block: true, Status: 403, Reason: "held"}}, false, true, 403, "held", nil, one},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var list []contentInspector
			for _, d := range c.in {
				list = append(list, &fixedInspector{dec: d})
			}
			got := chainInspectors(list...).Inspect(context.Background(), claudeapi.ContentInspectionInput{})
			var kinds []string
			for _, f := range got.Findings {
				kinds = append(kinds, f.Kind)
			}
			if got.Forward != c.wantForward || got.Block != c.wantBlock || got.Status != c.wantStatus || got.Reason != c.wantReason {
				t.Fatalf("verdict = forward %v block %v status %d reason %q; want %v %v %d %q",
					got.Forward, got.Block, got.Status, got.Reason, c.wantForward, c.wantBlock, c.wantStatus, c.wantReason)
			}
			if !reflect.DeepEqual(kinds, c.wantKinds) {
				t.Fatalf("finding kinds = %v, want %v", kinds, c.wantKinds)
			}
			if got.Meter != c.wantMeter {
				t.Fatalf("meter = %+v, want %+v", got.Meter, c.wantMeter)
			}
		})
	}
}

// Every inspector sees the content even when an earlier one denied: a later one's findings
// and meter are still owed.
func TestChainInspectorsAsksEveryoneAfterADeny(t *testing.T) {
	first := &fixedInspector{dec: claudeapi.ContentInspectionDecision{Status: 403, Reason: "no"}}
	second := &fixedInspector{dec: claudeapi.ContentInspectionDecision{Forward: true}}
	chainInspectors(first, second).Inspect(context.Background(), claudeapi.ContentInspectionInput{})
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("calls = %d, %d; want each inspector asked once", first.calls, second.calls)
	}
}

// The content-firewall status says whether the content firewall is attached. A decoy engine
// is an inspector on the same port, but it is not the firewall: alone it must read absent,
// and beside a firewall the state is the firewall's.
func TestContentFirewallStatusIgnoresADecoyOnlyInspector(t *testing.T) {
	firewall := &fixedInspector{}
	engine := &decoyOnlyEngine{}
	for name, tc := range map[string]struct {
		inspector contentInspector
		want      inferenceproxy.ContentFirewallState
	}{
		"nothing":               {nil, inferenceproxy.ContentFirewallInspectorAbsent},
		"a firewall":            {firewall, inferenceproxy.ContentFirewallInspectorAttached},
		"the decoy engine only": {chainInspectors(nil, engine), inferenceproxy.ContentFirewallInspectorAbsent},
		"a firewall and decoys": {chainInspectors(firewall, engine), inferenceproxy.ContentFirewallInspectorAttached},
	} {
		t.Run(name, func(t *testing.T) {
			b := &messagesInspectorBinding{}
			b.record(&inferencepep.Decider{Inspector: tc.inspector})
			if got := b.ContentFirewallState(); got != tc.want {
				t.Fatalf("state = %v, want %v", got, tc.want)
			}
		})
	}
}

// The decoy engine reaches a request only through the inspector of the hook PEP and of the
// Messages proxy. Both listeners need a launched run's credential before they inspect
// anything, so the two construction sites are pinned the way the content firewall's
// recording sites are: each hands the decider the chain of its own edition port and
// eng.sessionDecoys, and nothing else.
func TestTheHookAndTheProxyChainTheDecoyEngineIntoTheirInspector(t *testing.T) {
	for file, port := range map[string]string{
		"claudehookpep.go":        "hookContentInspector",
		"inferenceproxyserver.go": "contentInspector",
	} {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		sites := 0
		ast.Inspect(parsed, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			if key, ok := kv.Key.(*ast.Ident); !ok || (key.Name != "Inspector" && key.Name != "InspectorLive") {
				return true
			}
			sites++
			call := chainCallOf(kv.Value)
			if call == nil || len(call.Args) != 2 || !(isIdent(call.Fun, "chainInspectors") || isIdent(call.Fun, "liveInspectorChain")) {
				t.Errorf("%s: an Inspector is not a chain of <edition gate> and eng.sessionDecoys", file)
				return true
			}
			if got := selectorPath(call.Args[1]); got != "eng.sessionDecoys" {
				t.Errorf("%s: the chain's second inspector is %q, want eng.sessionDecoys", file, got)
			}
			if !callsSelector(call.Args[0], "thisEdition."+port+".get") {
				t.Errorf("%s: the chain's first inspector is not the %s edition port", file, port)
			}
			return true
		})
		if sites != 1 {
			t.Errorf("%s: %d Inspector assignments, want exactly the one the decoy engine is chained into", file, sites)
		}
	}
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// chainCallOf returns the call an Inspector value is built from: the value itself, or the
// call inside the adapter a hook wraps around it (hookInspectorSource).
func chainCallOf(e ast.Expr) *ast.CallExpr {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return nil
	}
	if isIdent(call.Fun, "hookInspectorSource") && len(call.Args) == 1 {
		return chainCallOf(call.Args[0])
	}
	return call
}

// callsSelector reports whether e calls the function a.b.c anywhere inside it, such as an
// edition port's get inside the gate closure of a chain.
func callsSelector(e ast.Expr, path string) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && selectorPath(call.Fun) == path {
			found = true
		}
		return !found
	})
	return found
}

// selectorPath renders a.b.c; anything else is "".
func selectorPath(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		if base := selectorPath(x.X); base != "" {
			return base + "." + x.Sel.Name
		}
	}
	return ""
}

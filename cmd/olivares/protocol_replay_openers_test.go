// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// protocolReplayPublishes are the kernel calls that publish a Message. Inside a
// replay they need directory evidence that only the prepared replay observes
// before its transaction opens.
var protocolReplayPublishes = map[string]bool{
	"ProjectProtocolReply":    true,
	"RecordProtocolInterrupt": true,
	"SendWorkflowWorkTask":    true,
}

// T9: every replay this package opens whose mutation reaches a publish is the
// prepared form. A plain ApplyProtocolReplay may publish only when it is
// nested inside a prepared replay's mutation, which declares its claim.
func TestEveryPublishingReplayOpenerIsPrepared(t *testing.T) {
	unprepared, _ := protocolReplayOpenerCensus(t)
	if len(unprepared) != 0 {
		t.Fatalf("replay openers that publish without preparing their evidence: %s", strings.Join(unprepared, ", "))
	}
}

// T9b: every replay this package opens is the prepared form, publishing or
// not, so no production transaction reads a participant or a reader identity
// through a module port inside itself. A plain ApplyProtocolReplay appears only
// nested inside a prepared replay's mutation, which declares its claim.
func TestEveryReplayOpenerIsPreparedOrNested(t *testing.T) {
	_, plain := protocolReplayOpenerCensus(t)
	if len(plain) != 0 {
		t.Fatalf("plain replay openers outside a prepared replay: %s", strings.Join(plain, ", "))
	}
}

// protocolReplayOpenerCensus parses this package's non-test sources and
// returns, by position, the plain replay openers outside a prepared replay
// whose mutation reaches a publish, and every plain replay opener outside a
// prepared replay.
func protocolReplayOpenerCensus(t *testing.T) (unprepared, plain []string) {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		t.Fatal("the census found no source files")
	}

	// calls maps every function and method name of the package to the names
	// it calls; a publish reached through any of them counts.
	calls := map[string]map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			names := calls[fn.Name.Name]
			if names == nil {
				names = map[string]bool{}
				calls[fn.Name.Name] = names
			}
			for name := range calledNames(fn.Body) {
				names[name] = true
			}
		}
	}
	reaches := map[string]bool{}
	var publishes func(name string, seen map[string]bool) bool
	publishes = func(name string, seen map[string]bool) bool {
		if protocolReplayPublishes[name] {
			return true
		}
		if done, ok := reaches[name]; ok {
			return done
		}
		if seen[name] {
			return false
		}
		seen[name] = true
		for callee := range calls[name] {
			if publishes(callee, seen) {
				reaches[name] = true
				return true
			}
		}
		return false
	}

	openers := 0
	for _, file := range files {
		var visit func(node ast.Node, insidePrepared bool)
		visit = func(node ast.Node, insidePrepared bool) {
			ast.Inspect(node, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := calledName(call)
				if name != "ApplyProtocolReplay" && name != "ApplyPreparedProtocolReplay" {
					return true
				}
				openers++
				mutation := call.Args[len(call.Args)-1]
				publishing := false
				for callee := range calledNames(mutation) {
					if publishes(callee, map[string]bool{}) {
						publishing = true
						break
					}
				}
				if name == "ApplyProtocolReplay" && !insidePrepared {
					plain = append(plain, fset.Position(call.Pos()).String())
					if publishing {
						unprepared = append(unprepared, fset.Position(call.Pos()).String())
					}
				}
				for _, arg := range call.Args[:len(call.Args)-1] {
					visit(arg, insidePrepared)
				}
				visit(mutation, insidePrepared || name == "ApplyPreparedProtocolReplay")
				return false
			})
		}
		visit(file, false)
	}
	if openers == 0 {
		t.Fatal("the census found no replay opener: it no longer sees the adapters")
	}
	sort.Strings(unprepared)
	sort.Strings(plain)
	return unprepared, plain
}

// calledName is the name a call expression invokes: the selector's name for a
// method or qualified call, the identifier otherwise.
func calledName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fun.Sel.Name
	case *ast.Ident:
		return fun.Name
	}
	return ""
}

func calledNames(node ast.Node) map[string]bool {
	names := map[string]bool{}
	ast.Inspect(node, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if name := calledName(call); name != "" {
				names[name] = true
			}
		}
		return true
	})
	return names
}

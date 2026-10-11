// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestGRPCAdmissionIsAnAdapterOfAdmit pins that the gRPC transport decides admission through
// admit and nowhere else. Every RPC's REST twin is an ungoverned tenant or entity route, so the
// two transports answer the same today (modules/governance TestGRPCRESTAuthorizationParity); a
// second decision path beside admit is how they drift apart: step-up, concealment, witness and
// owner audit reach one door and not the other.
func TestGRPCAdmissionIsAnAdapterOfAdmit(t *testing.T) {
	var files []string
	for _, pattern := range []string{"grpc*.go", "ingest*.go"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range matches {
			if !strings.HasSuffix(m, "_test.go") {
				files = append(files, m)
			}
		}
	}
	fset := token.NewFileSet()
	admits := 0
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch {
			case sel.Sel.Name == "admit":
				admits++
			case strings.HasPrefix(sel.Sel.Name, "Authorize"):
				t.Errorf("%s: %s decides admission beside admit", fset.Position(sel.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
	if admits != 1 {
		t.Errorf("the gRPC transport calls admit %d times, want once (grpcAuthorizeResource)", admits)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// hcl-module-guard parses every native Terraform file in one root module with
// HashiCorp's HCL parser and verifies the invariants needed by check-aws-estate.sh.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

const prefix = "hcl-module-guard"

func finding(format string, args ...any) {
	fmt.Fprintf(os.Stderr, prefix+": FAIL — "+format+"\n", args...)
	os.Exit(1)
}

func cannot(format string, args ...any) {
	fmt.Fprintf(os.Stderr, prefix+": COULD NOT LOOK — "+format+"\n", args...)
	os.Exit(2)
}

func main() {
	if len(os.Args) != 2 {
		cannot("usage: hcl-module-guard <terraform-root-directory>")
	}

	root := os.Args[1]
	entries, err := os.ReadDir(root)
	if err != nil {
		cannot("cannot read %s: %v", root, err)
	}

	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".tf.json") {
			finding("root contains %s, which this native-HCL guard cannot ignore", entry.Name())
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tf") {
			continue
		}
		paths = append(paths, root+string(os.PathSeparator)+entry.Name())
	}
	if len(paths) == 0 {
		finding("%s has no native .tf files", root)
	}
	sort.Strings(paths)

	moduleCount := 0
	moduleNames := make(map[string]struct{})
	ingressCount := 0
	backendLocked := false
	backendSeen := false
	for _, path := range paths {
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			cannot("cannot read %s: %v", path, readErr)
		}

		file, diagnostics := hclsyntax.ParseConfig(src, path, hcl.Pos{Line: 1, Column: 1})
		if diagnostics.HasErrors() {
			finding("invalid HCL: %s", compactDiagnostics(diagnostics))
		}

		body, ok := file.Body.(*hclsyntax.Body)
		if !ok {
			cannot("parser returned an unexpected body for %s", path)
		}

		for _, block := range body.Blocks {
			// ⛔ EL BLOQUEO DE ESTADO DEL BACKEND, Y POR QUÉ SE COMPRUEBA AQUÍ.
			// Vivía como `-backend-config="use_lockfile=true"` en el `run:` del paso de
			// apply, y la invariante se verificaba buscando esa cadena en el texto del
			// paso. El contraste `sol max` del 2026-08-27 (C-01) lo rompió con un mutante
			// de una línea: `echo use_lockfile=true` junto a un flag real en `false`
			// satisfacía la comprobación. Una propiedad que un `echo` puede fingir no es
			// una propiedad. En HCL no se puede: un comentario no es un atributo, y este
			// parser lee el ÁRBOL.
			//
			// Sin lock, dos applies concurrentes sobre el mismo `terraform.tfstate` no
			// dan error: intercalan escrituras y dejan un estado que describe una
			// infraestructura que no existe. Se acepta `use_lockfile = true` o un
			// `dynamodb_table` no vacío: la invariante es el BLOQUEO, no una
			// implementación — exigir una obligaría a rediseñar a quien elija la otra
			// con su razón escrita.
			if block.Type == "terraform" {
				for _, inner := range block.Body.Blocks {
					if inner.Type != "backend" {
						continue
					}
					backendSeen = true
					if attribute, exists := inner.Body.Attributes["use_lockfile"]; exists {
						value, diagnostics := attribute.Expr.Value(nil)
						if !diagnostics.HasErrors() && value.Type() == cty.Bool && value.True() {
							backendLocked = true
						}
					}
					if attribute, exists := inner.Body.Attributes["dynamodb_table"]; exists {
						value, diagnostics := attribute.Expr.Value(nil)
						if !diagnostics.HasErrors() && value.Type() == cty.String &&
							strings.TrimSpace(value.AsString()) != "" {
							backendLocked = true
						}
					}
				}
				continue
			}
			if block.Type != "module" {
				continue
			}
			moduleCount++
			if len(block.Labels) != 1 {
				finding("module block at %s must have exactly one label", block.TypeRange.String())
			}
			name := block.Labels[0]
			if _, exists := moduleNames[name]; exists {
				finding("root repeats module %q across its .tf files", name)
			}
			moduleNames[name] = struct{}{}

			// Duplicate attributes are parser errors. Reading expressions from the
			// AST makes comments and whitespace irrelevant to the active wiring.
			if name == "ingress" {
				ingressCount++
				requireTraversal(block, "access_logs_bucket", "module.data.plane_bucket_id")
				requireTraversal(block, "connection_logs_bucket", "module.data.alb_conn_bucket_id")
			}
		}
	}

	if moduleCount == 0 {
		finding("%s has no module blocks", root)
	}
	if ingressCount != 1 {
		finding("root must contain exactly one module %q block; found %d", "ingress", ingressCount)
	}
	if !backendSeen {
		finding("root declares no terraform backend block; the state would be local to whichever " +
			"runner happened to apply, and nothing would serialise two applies")
	}
	if backendSeen && !backendLocked {
		finding("the terraform backend declares no state locking (neither use_lockfile = true nor a " +
			"dynamodb_table): two concurrent applies would interleave writes over the same tfstate")
	}

	fmt.Printf("root-wiring-ok — %d parsed module block(s) across %d .tf file(s), backend state locking declared in HCL\n",
		moduleCount, len(paths))
}

func requireTraversal(block *hclsyntax.Block, attributeName, want string) {
	attribute, exists := block.Body.Attributes[attributeName]
	if !exists {
		finding("module %q has no active %s argument", block.Labels[0], attributeName)
	}

	traversal, diagnostics := hcl.AbsTraversalForExpr(attribute.Expr)
	if diagnostics.HasErrors() {
		finding("module %q argument %s is not a direct traversal: %s",
			block.Labels[0], attributeName, compactDiagnostics(diagnostics))
	}
	got, ok := traversalName(traversal)
	if !ok || got != want {
		finding("module %q argument %s is %q, want %q",
			block.Labels[0], attributeName, got, want)
	}
}

func traversalName(traversal hcl.Traversal) (string, bool) {
	parts := make([]string, 0, len(traversal))
	for _, step := range traversal {
		switch typed := step.(type) {
		case hcl.TraverseRoot:
			parts = append(parts, typed.Name)
		case hcl.TraverseAttr:
			parts = append(parts, typed.Name)
		default:
			return "", false
		}
	}
	return strings.Join(parts, "."), len(parts) > 0
}

func compactDiagnostics(diagnostics hcl.Diagnostics) string {
	parts := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		if diagnostic == nil || diagnostic.Severity != hcl.DiagError {
			continue
		}
		where := ""
		if diagnostic.Subject != nil {
			where = diagnostic.Subject.String() + ": "
		}
		parts = append(parts, where+diagnostic.Summary)
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return "unknown parser error"
	}
	return strings.Join(parts, "; ")
}

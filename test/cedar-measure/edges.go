// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
)

// Characters the edge set needs, written as code points so no source escape
// can be rewritten on the way.
var (
	lf        = string(rune(0x0a))
	cr        = string(rune(0x0d))
	vt        = string(rune(0x0b))
	ff        = string(rune(0x0c))
	nul       = string(rune(0x00))
	nbsp      = string(rune(0x00a0))
	nel       = string(rune(0x0085))
	bom       = string(rune(0xfeff))
	backslash = string(rune(0x5c))
)

type edgeCase struct {
	name string
	text string
}

// basePrefix is the text the product places before an operator policy when it
// builds an evaluator.
const basePrefix = "permit(principal, action, resource);"

// mergeSources composes texts the way the product composes a Cedar union:
// each part trimmed of Unicode space, non-empty parts joined by two line feeds.
func mergeSources(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			kept = append(kept, t)
		}
	}
	return strings.Join(kept, lf+lf)
}

// edgeCases returns the lexical, tokenizer and product-composition edges. The
// library decides each one; the framer must agree.
func edgeCases() []edgeCase {
	const p = "permit(principal, action, resource)"
	const f = "forbid(principal, action, resource)"
	lexical := []edgeCase{
		{"semicolon-in-string", p + ` when { resource.name == "a;b" };` + lf + f + ";"},
		{"semicolon-in-line-comment", "// note; not a boundary" + lf + p + ";"},
		{"semicolon-and-quote-in-block-comment", `/* a; "b; */ ` + p + ";"},
		{"escaped-quote-in-annotation", `@id("x` + backslash + `";y") ` + p + ";"},
		{"escaped-unicode-semicolon", p + ` when { resource.n == "` + backslash + `u{3b}" };`},
		{"comment-openers-in-strings", p + ` when { resource.a == "//" && resource.b == "/*" };` + f + ";"},
		{"escaped-backslash-before-quote", p + ` when { resource.p == "a` + backslash + backslash + `" };` + p + ";"},
		{"trailing-comment", p + "; // end"},
		{"trailing-tokens", p + "; forbid(principal"},
		{"no-terminator", p},
		{"empty-document", ""},
		{"empty-statement", p + ";;"},
		{"unterminated-block-comment", "/* " + p + ";"},
		{"raw-newline-in-string", p + ` when { resource.a == "x` + lf + `;" };`},
		{"block-comment-closed-by-first", "/*/ ; */" + p + ";"},
		{"last-policy-names-a-principal", p + ";" + lf + `forbid(principal == User::"last", action, resource);`},
	}
	tokenizer := []edgeCase{
		{"cr-inside-line-comment", "// a" + cr + p + ";"},
		{"cr-comment-after-policy", p + "; // x" + cr + p + ";"},
		{"nul-in-code", p + nul + ";"},
		{"nul-in-comment", "// " + nul + lf + p + ";"},
		{"nul-in-string", p + ` when { context.tenant == "` + nul + `" };`},
		{"bom-at-start", bom + p + ";"},
		{"bom-between-statements", p + ";" + bom + p + ";"},
		{"bom-in-comment", "// " + bom + lf + p + ";"},
		{"bom-in-string", p + ` when { context.tenant == "` + bom + `" };`},
		{"vt-between-statements", p + ";" + vt + p + ";"},
		{"ff-between-statements", p + ";" + ff + p + ";"},
		{"nbsp-between-statements", p + ";" + nbsp + p + ";"},
		{"vt-tail", p + ";" + vt},
		{"ff-tail", p + ";" + ff},
		{"nbsp-tail", p + ";" + nbsp},
		{"nel-tail", p + ";" + nel},
		{"vt-inside-comment-tail", p + "; // x" + vt},
		{"slash-at-end", p + ";/"},
		{"blank-tail", p + "; " + "\t" + cr + lf},
	}
	var product []edgeCase
	for _, e := range lexical {
		product = append(product, edgeCase{"base-prefix+" + e.name, basePrefix + lf + e.text})
	}
	product = append(product,
		edgeCase{"union-trimmed-parts", mergeSources(p+";"+nbsp, vt+f+";", "")},
		edgeCase{"union-three-parts", mergeSources(p+";", f+` when { resource.kind == "a;b" };`, "// adopted"+lf+p+";")},
		edgeCase{"union-comment-at-end", mergeSources(p+"; // authored", f+";")},
	)
	all := append(lexical, tokenizer...)
	return append(all, product...)
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "strings"

// THE PROSE OF A PUBLISHED OPERATION.
//
// Both documents carried a summary and nothing else until 2026-08-16: 757 operations,
// zero descriptions. For the stable contract that meant a line like "List agents"; for
// the beta contract it meant "finops module route (requires finops:spend:read)" — the
// registration restated, which tells an integrator which permission to grant and
// nothing about what the call does.
//
// The text now stamped here comes from operationDescriptions
// (openapi_op_descriptions.gen.go), which is GENERATED. A module route uses the doc
// comment of its registered handler when publishable, or its row in
// scripts/openapi-op-catalog.tsv; the hand-built stable operations use that same catalog.
// Keeping the table generated is what lets the beta document stay a reflector:
// a module that registers a documented handler gets its published prose the same way it
// gets the route, without editing this package.
//
// A beta summary replaces the registration summary only when the generated provenance
// set proves that the description came directly from the registered handler's Go doc
// comment. It normally uses that prose's first complete top-level sentence. If
// different handler descriptions begin with the same sentence, their complete text is
// retained so shortening does not erase a real distinction. Identical handler prose
// remains identical; catalog-only operations keep the registration summary because a
// reviewed catalog sentence is not proof of what the handler consumes or returns.
//
// Regenerate with `bash scripts/check-openapi-op-descriptions.sh --write` followed by
// `task openapi:dump && pnpm --dir web run codegen`; the gate behind
// `task lint:public-counts` fails, naming the operation, when the table and the
// published documents disagree with the code.

// operationMethods are the path-item keys that are operations. The others (parameters,
// summary, $ref, servers) are not, and stamping a description on one would corrupt the
// document.
var operationMethods = map[string]bool{
	"GET": true, "PUT": true, "POST": true, "DELETE": true,
	"PATCH": true, "HEAD": true, "OPTIONS": true, "TRACE": true,
}

// applyOperationDescriptions stamps each operation of paths with its published
// description. It is the shared decorator both builders call, in the shape
// applyStabilityTier and stampCorePermissions already use.
//
// An operation with no entry is left WITHOUT a description rather than given a filler:
// a generic sentence would be indistinguishable, in the document and in every generated
// client, from a real one — and the gate that reads the published documents would then
// report full coverage over prose nobody wrote.
func applyOperationDescriptions(paths map[string]any) {
	summaries := moduleOperationSummaries(
		operationDescriptions,
		handlerDocOperationDescriptions,
	)
	for path, item := range paths {
		ops, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for method, raw := range ops {
			m := strings.ToUpper(method)
			if !operationMethods[m] {
				continue
			}
			op, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			key := m + " " + path
			if d, ok := operationDescriptions[key]; ok && d != "" {
				op["description"] = d
				if summary, ok := summaries[key]; ok {
					op["summary"] = summary
				}
			}
		}
	}
}

// moduleOperationSummaries derives beta summaries solely from descriptions whose
// generated provenance is handler-doc. Stable and catalog-only summaries are excluded.
func moduleOperationSummaries(
	descriptions map[string]string,
	handlerDocKeys map[string]struct{},
) map[string]string {
	type source struct {
		full  string
		short string
	}

	sources := make(map[string]source)
	variants := make(map[string]map[string]struct{})
	for key := range handlerDocKeys {
		_, path, ok := strings.Cut(key, " ")
		if !ok || !strings.HasPrefix(path, "/v1/m/") {
			continue
		}
		description, ok := descriptions[key]
		if !ok {
			continue
		}
		full := normalizeOperationProse(description)
		if full == "" {
			continue
		}
		short := firstOperationSentence(full)
		sources[key] = source{full: full, short: short}
		if variants[short] == nil {
			variants[short] = make(map[string]struct{})
		}
		variants[short][full] = struct{}{}
	}

	summaries := make(map[string]string, len(sources))
	for key, source := range sources {
		summary := source.short
		if len(variants[source.short]) > 1 {
			summary = source.full
		}
		summaries[key] = summary
	}
	return summaries
}

func normalizeOperationProse(prose string) string {
	return strings.Join(strings.Fields(prose), " ")
}

// firstOperationSentence stops only at a top-level sentence boundary. A period in a
// parenthetical qualifier, bracketed value, quoted literal, or version number belongs
// to the handler's sentence and must not silently change its meaning.
func firstOperationSentence(prose string) string {
	depth := 0
	quoted := false
	for i, r := range prose {
		switch {
		case r == '"':
			quoted = !quoted
		case !quoted && (r == '(' || r == '[' || r == '{'):
			depth++
		case !quoted && (r == ')' || r == ']' || r == '}'):
			if depth > 0 {
				depth--
			}
		case !quoted && depth == 0 && r == '.' && i+1 < len(prose) && prose[i+1] == ' ':
			return prose[:i+1]
		}
	}
	return prose
}

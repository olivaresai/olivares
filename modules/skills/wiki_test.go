// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"context"
	"strings"
	"testing"
)

func TestBuiltinWikiPackIncludesSchemaAndAllOperations(t *testing.T) {
	p, s, err := importBuiltin(context.Background(), "olivares-wiki")
	if err != nil {
		t.Fatal(err)
	}
	if s.Kind != "builtin" || len(p.Members) != 1 || p.Members[0].Name != "olivares-wiki" {
		t.Fatalf("wrong wiki pack: %+v", s)
	}
	for name, terms := range map[string][]string{
		"olivares-wiki/SKILL.md":  {"ingest", "query", "lint", "raw/", "index.md", "log.md", "wiki/lint-", "citations"},
		"olivares-wiki/schema.md": {"immutable", "tool-owned", "one line per page", "## [YYYY-MM-DD] op | title"},
	} {
		body, ok := p.File(name)
		if !ok {
			t.Fatalf("missing %s", name)
		}
		for _, term := range terms {
			if !strings.Contains(string(body), term) {
				t.Errorf("%s lacks %q", name, term)
			}
		}
	}
}

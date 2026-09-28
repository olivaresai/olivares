// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Every None reason of the credential-binding relation cites the core/auth
// lines that read its column: the subject compare, the credential reload, the
// successor test, the ceiling rule or the seal (the principal_decl.go:232
// rule). The composition census checks only that a cited line exists; this
// checks what the line shows.
func TestCredentialBindingNoneCitationsShowTheirReader(t *testing.T) {
	ceiling := []string{"func (c credentialCeiling) within("}
	readers := map[string][]string{
		"target_tenant_id":     {"row.TargetTenantID != s.Tenant"},
		"subject_kind":         {"row.SubjectKind != s.Kind"},
		"subject_ref":          {"row.SubjectRef != s.Ref"},
		"credential_kind":      {"switch pin.kind"},
		"credential_id":        {"Get(ctx, pin.id)", "Get(ctx, ref.credentialID)"},
		"ceiling_kind":         ceiling,
		"ceiling_workspace_id": ceiling,
		"ceiling_agent":        ceiling,
		"superseded_by":        {"!row.SupersededBy.IsZero()"},
		"seal":                 {"func credentialBindingSeal(", "bytes.Equal(row.Seal, seal[:])"},
	}
	citation := regexp.MustCompile(`(core/auth/[a-z_]+\.go):(\d+)`)
	checked := 0
	for _, f := range credentialBindingDescriptor.Fields {
		want, ok := readers[f.Name]
		if !ok {
			continue
		}
		if f.Principal == nil {
			t.Fatalf("%s declares no principal form", f.Name)
		}
		cites := citation.FindAllStringSubmatch(f.Principal.Reason, -1)
		if len(cites) == 0 {
			t.Fatalf("%s cites no core/auth line: %q", f.Name, f.Principal.Reason)
		}
		for _, m := range cites {
			n, _ := strconv.Atoi(m[2])
			line := sourceLine(t, filepath.Join("..", "..", "..", "..", m[1]), n)
			shows := false
			for _, token := range want {
				shows = shows || strings.Contains(line, token)
			}
			if !shows {
				t.Errorf("%s cites %s:%d, which is %q, not its reader (%q)", f.Name, m[1], n, strings.TrimSpace(line), want)
			}
			checked++
		}
	}
	if checked < len(readers) {
		t.Fatalf("checked %d citations, want at least one for each of %d columns", checked, len(readers))
	}
}

func sourceLine(t *testing.T, path string, want int) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	lines := bufio.NewScanner(file)
	for n := 1; lines.Scan(); n++ {
		if n == want {
			return lines.Text()
		}
	}
	t.Fatalf("%s has no line %d", path, want)
	return ""
}

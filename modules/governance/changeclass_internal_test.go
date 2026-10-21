// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestTreeMatchReadsAGlobLikeAGitignoreLine(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		want          bool
	}{
		{"CODEOWNERS", "CODEOWNERS", true},
		{"CODEOWNERS", ".github/CODEOWNERS", true},
		{"codeowners", "docs/CODEOWNERS", true},
		{"Dockerfile*", "build/Dockerfile.prod", true},
		{"Dockerfile*", "build/dockerfile", true},
		{"*.Dockerfile", "svc/app.Dockerfile", true},
		{"requirements*.txt", "svc/requirements-dev.txt", true},
		{".github/workflows/**", ".github/workflows/ci.yml", true},
		{".github/workflows/**", ".github/workflows/sub/ci.yml", true},
		{".github/workflows/**", ".github/workflows", false},
		{".github/workflows/**", "docs/.github/workflows/ci.yml", false},
		// A pattern that names a directory covers what is below it.
		{".github/workflows", ".github/workflows/ci.yml", true},
		{"infra/*", "infra/a/b.tf", true},
		{"secrets", "secrets/key.pem", true},
		{"**/migrations/**", "migrations/001.sql", true},
		{"**/migrations/**", "svc/db/migrations/001.sql", true},
		{"**/migrations/**", "svc/migrationsx/001.sql", false},
		{"go.mod", "svc/go.modx", false},
	} {
		if got := treeMatch(tc.pattern, tc.path); got != tc.want {
			t.Errorf("treeMatch(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestValidGlobRefusesWhatCouldNeverMatch(t *testing.T) {
	for g, want := range map[string]bool{
		"infra/**": true, "*.tf": true, "a/b[0-9]/c": true, ".github/workflows": true,
		"": false, "/etc": false, "infra/": false, "./infra": false, "a/../b": false, "a//b": false, "[a": false, "**/**/**/x": false,
	} {
		if got := validGlob(g); got != want {
			t.Errorf("validGlob(%q) = %v, want %v", g, got, want)
		}
	}
}

func TestClassifyChangeByPaths(t *testing.T) {
	for _, tc := range []struct {
		name      string
		paths     []string
		wantClass string
		wantPaths []string
	}{
		{"nothing", nil, ChangeClassDocsOnly, []string{}},
		{"docs", []string{"README.md", "docs/guide/intro.rst", "LICENSE"}, ChangeClassDocsOnly, []string{"README.md", "docs/guide/intro.rst", "LICENSE"}},
		{"code", []string{"README.md", "src/a.go"}, ChangeClassCode, []string{"src/a.go"}},
		{"workflow", []string{"src/a.go", ".github/workflows/ci.yml"}, ChangeClassSensitive, []string{".github/workflows/ci.yml"}},
		{"deleted workflow", []string{".github/workflows/ci.yml"}, ChangeClassSensitive, []string{".github/workflows/ci.yml"}},
		// A rename arrives as its old and its new path.
		{"rename into workflows", []string{"docs/ci.md", ".github/workflows/ci.yml"}, ChangeClassSensitive, []string{".github/workflows/ci.yml"}},
		{"rename out of workflows", []string{".github/workflows/ci.yml", "docs/ci.md"}, ChangeClassSensitive, []string{".github/workflows/ci.yml"}},
		{"lockfile", []string{"web/package-lock.json"}, ChangeClassSensitive, []string{"web/package-lock.json"}},
		{"requirements are not docs", []string{"requirements.txt"}, ChangeClassSensitive, []string{"requirements.txt"}},
		{"nested migration", []string{"svc/migrations/002.sql"}, ChangeClassSensitive, []string{"svc/migrations/002.sql"}},
		// Files that look like documentation and are not.
		{"build recipe in txt", []string{"CMakeLists.txt"}, ChangeClassCode, []string{"CMakeLists.txt"}},
		{"code under docs", []string{"docs/conf.py", "docs/build.sh"}, ChangeClassCode, []string{"docs/conf.py", "docs/build.sh"}},
		{"code under a directory named like a document", []string{"notes.md/run.sh"}, ChangeClassCode, []string{"notes.md/run.sh"}},
		{"agent instructions", []string{"AGENTS.md"}, ChangeClassSensitive, []string{"AGENTS.md"}},
		{"agent skill", []string{".claude/commands/deploy.md"}, ChangeClassSensitive, []string{".claude/commands/deploy.md"}},
		{"abnormal path", []string{"docs/../.github/workflows/x.yml", "README.md"}, ChangeClassSensitive, []string{"docs/../.github/workflows/x.yml"}},
		{"inside .git", []string{"sub/.git/hooks/pre-commit"}, ChangeClassSensitive, []string{"sub/.git/hooks/pre-commit"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyChange(tc.paths, defaultSensitivePaths)
			if got.Class != tc.wantClass || !reflect.DeepEqual(got.Paths, tc.wantPaths) {
				t.Fatalf("classifyChange(%q) = %+v, want %s %q", tc.paths, got, tc.wantClass, tc.wantPaths)
			}
		})
	}
}

// What a reader sees is printable, bounded and masked; what decided the class
// was the real path.
func TestShownPathIsSafeToRender(t *testing.T) {
	long := strings.Repeat("a", 400)
	for _, tc := range []struct{ in, want string }{
		{"src/a.go", "src/a.go"},
		{"src/caf\u00e9.go", "src/caf\u00e9.go"},
		{"a\nchange class: docs_only", `"a\nchange class: docs_only"`},
		{"a\u202eb", `"a\u202eb"`},
		{"a\u2028b", `"a\u2028b"`},
		{"a\xffb", `"a\xffb"`},
	} {
		if got := shownPath(tc.in); got != tc.want {
			t.Errorf("shownPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := shownPath(long); len(got) > maxShownPath+len("…") || !strings.HasSuffix(got, "…") {
		t.Errorf("a %d byte path shows as %d bytes %q", len(long), len(got), got[len(got)-4:])
	}
	if got := shownPath("keys/ghp" + "_" + strings.Repeat("a1B2c3D4e5", 4) + "f6.txt"); strings.Contains(got, "a1B2c3D4e5") {
		t.Errorf("a token-shaped file name is shown as %q", got)
	}
}

// The ledger entry of a classified request names the class and its paths; one
// no policy classified keeps the entry it always had.
func TestApprovalCreateMetaCarriesTheChangeClass(t *testing.T) {
	plain := approvalCreateMeta(createApprovalRequest{Action: "gitpublish.push", SubjectKind: "gitpublish.target"}, 1, RiskTierHigh)
	if _, ok := plain["change_class"]; ok || len(plain) != 4 {
		t.Fatalf("unclassified meta = %v, want the four keys it always had", plain)
	}
	in := createApprovalRequest{Action: "gitpublish.push", SubjectKind: "gitpublish.target", ChangeClass: ChangeClassSensitive, ChangePaths: []string{"go.mod"}}
	got := approvalCreateMeta(in, 2, RiskTierHigh)
	if got["change_class"] != ChangeClassSensitive || !reflect.DeepEqual(got["change_paths"], []string{"go.mod"}) || got["required_approvals"] != int64(2) {
		t.Fatalf("classified meta = %v", got)
	}
}

func TestClassifyChangeBoundsTheShownPaths(t *testing.T) {
	var paths []string
	for i := 0; i < maxChangePaths+10; i++ {
		paths = append(paths, fmt.Sprintf("src/f%d.go", i))
	}
	got := classifyChange(paths, defaultSensitivePaths)
	if got.Class != ChangeClassCode || len(got.Paths) != maxChangePaths || got.More != 10 {
		t.Fatalf("class %s with %d paths and %d more, want code with %d and 10", got.Class, len(got.Paths), got.More, maxChangePaths)
	}
}

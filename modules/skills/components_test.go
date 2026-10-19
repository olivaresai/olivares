// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/skills"
)

func componentArchive(t *testing.T, suffix string) []byte {
	t.Helper()
	return archive(t, []string{"research/SKILL.md", "agents/research.md", "commands/research.md", "rules/common/testing.md"}, []string{harmless,
		"---\nname: research\ndescription: Review evidence.\ntools: Read, Bash\n---\nAgent " + suffix,
		"---\ndescription: Review evidence.\nallowed-tools: Bash\n---\nCommand " + suffix,
		"# Testing\nRule " + suffix})
}

func TestPackComponentsAreImmutableMembersWithApprovalAndDiff(t *testing.T) {
	old, err := skills.ImportArchive(t.Context(), bytes.NewReader(componentArchive(t, "old")), "zip", "")
	if err != nil {
		t.Fatalf("component pack refused: %v", err)
	}
	next, err := skills.ImportArchive(t.Context(), bytes.NewReader(componentArchive(t, "new")), "zip", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(old.Members) != 4 {
		t.Fatalf("members: %+v", old.Members)
	}
	active := map[string]bool{}
	for _, m := range old.Members {
		active[m.Name] = len(m.ActiveContent()) > 0
		if strings.Contains(m.Name, ":") && m.Kind != strings.SplitN(m.Name, ":", 2)[0] {
			t.Fatalf("member kind missing: %+v", m)
		}
	}
	if !active["agent:research"] || !active["command:research"] || active["rule:common/testing"] || active["research"] {
		t.Fatalf("approval: %v", active)
	}
	diff := skills.DiffPacks(old, next)
	if len(diff.Members) != 3 || len(diff.Files) != 3 {
		t.Fatalf("component update diff: %+v", diff)
	}
}

func TestComponentsUseSameAssignmentPrecedenceAndVerifiedDelivery(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: componentAuthority{}})
	install := func(name, body string) skills.InstallResult {
		r := h.upload(t, name, name, "", componentArchive(t, body))
		var p skills.InstallResult
		if r.Code != http.StatusCreated || json.Unmarshal(r.Body.Bytes(), &p) != nil {
			t.Fatalf("install: %d %s", r.Code, r.Body.String())
		}
		return p
	}
	broad, narrow := install("generic-components", "generic"), install("project-components", "project")
	workspace := h.workspace(t, false)
	folder := skills.Target{Kind: "folder", ID: model.NewID().String()}
	for _, p := range []struct {
		target   skills.Target
		revision string
	}{{workspace, broad.Revision.ID}, {folder, narrow.Revision.ID}} {
		req := skills.AssignmentRequest{Target: p.target, RevisionID: p.revision}
		if r := h.request("POST", "/v1/m/skills/assignments", req, ""); r.Code != http.StatusBadRequest {
			t.Fatalf("unapproved active component: %d %s", r.Code, r.Body.String())
		}
		req.ApproveActiveContent = true
		if r := h.request("POST", "/v1/m/skills/assignments", req, ""); r.Code != http.StatusCreated {
			t.Fatalf("approved assignment: %d %s", r.Code, r.Body.String())
		}
	}
	selection, err := h.selection(workspace, folder)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range selection.Members {
		if m.Name != "research" && (m.PackID != narrow.Pack.ID || len(m.Overrides) != 1) {
			t.Fatalf("overlay: %+v", m)
		}
	}
	err = h.store.View(context.Background(), h.tenant, func(sc store.Scope) error {
		files, err := h.module.ReadSelectionFiles(t.Context(), sc, selection)
		if err != nil {
			return err
		}
		if len(files) != 4 {
			t.Fatalf("delivered: %+v", files)
		}
		for _, f := range files {
			if strings.Contains(string(f.Bytes), "generic") {
				t.Fatalf("generic bytes survived overlay: %s", f.Path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStandaloneComponentsKeepNativeBodiesAndRejectAmbiguousFiles(t *testing.T) {
	for _, c := range []struct{ path, body, name string }{
		{"agents/review.md", "---\nname: review\ndescription: Review changes.\n---\nRead.", "agent:review"},
		{"commands/review.md", "# Review\nRead changes.", "command:review"},
		{"rules/common/testing.md", "# Testing\nTest changes.", "rule:common/testing"},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, wrapper := range []string{"", "pack/"} {
				p, err := skills.ImportArchive(t.Context(), bytes.NewReader(archive(t, []string{wrapper + c.path}, []string{c.body})), "zip", "")
				if err != nil || len(p.Members) != 1 || p.Members[0].Name != c.name {
					t.Fatalf("%s: %+v %v", wrapper, p, err)
				}
				b, ok := p.File(c.path)
				if !ok || string(b) != c.body {
					t.Fatal("instruction body changed")
				}
			}
		})
	}
	for _, path := range []string{"agents/README.md", "agents/nested/review.md", "commands/run.sh", "settings.json", "rules/common/testing.txt"} {
		p, err := skills.ImportArchive(t.Context(), bytes.NewReader(archive(t, []string{path}, []string{"text"})), "zip", "")
		if err == nil || p != nil {
			t.Fatalf("unsafe or ambiguous component accepted: %s", path)
		}
	}
}

func TestComponentSelectionRejectsSameRankConflict(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: componentAuthority{}})
	target := h.workspace(t, false)
	for _, body := range []string{"one", "two"} {
		r := h.upload(t, "same-rank-"+body, body, "", archive(t, []string{"rules/common/testing.md"}, []string{"# Testing\n" + body}))
		var p skills.InstallResult
		if r.Code != http.StatusCreated || json.Unmarshal(r.Body.Bytes(), &p) != nil {
			t.Fatalf("install: %d %s", r.Code, r.Body.String())
		}
		h.assign(t, target, p.Revision.ID)
	}
	if _, err := h.selection(target); err == nil {
		t.Fatal("conflicting rules at the same target were accepted")
	}
}

type componentAuthority struct{}

func (componentAuthority) ResolveSkillsTarget(ctx context.Context, sc store.Scope, p auth.Principal, target skills.Target, write bool) (skills.StoredTarget, error) {
	if target.Kind == "folder" {
		return skills.StoredTarget{Target: target, Version: 1}, nil
	}
	return (directoryAuthority{}).ResolveSkillsTarget(ctx, sc, p, target, write)
}

func TestLegacySkillPackWrappersCanUseComponentDirectoryNames(t *testing.T) {
	for _, wrapper := range []string{"agents", "commands", "rules", "rules/shared"} {
		p, err := skills.ImportArchive(t.Context(), bytes.NewReader(archive(t, []string{wrapper + "/research/SKILL.md", wrapper + "/README.md"}, []string{harmless, "Documentation"})), "zip", "")
		if err != nil || len(p.Members) != 1 || p.Members[0].Name != "research" {
			t.Fatalf("legacy wrapper %s: %+v %v", wrapper, p, err)
		}
	}
}

func TestNativeRuleFileImportsNeedApprovalIncludingUnicodeWhitespace(t *testing.T) {
	for i, tc := range []struct {
		body   string
		active bool
	}{
		{"Read @./private.md", true},
		{"---\ndescription: \" @/work/private.md\"\n---\nRule", true},
		{"\v@/work/private.md", true},
		{"@/work/private.md", true},
		{"\u00a0@./private.md", true},
		{"```\n@./example.md\n```", true},
		{"Contact alice@example.test", false},
	} {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			p, err := skills.ImportArchive(t.Context(), bytes.NewReader(archive(t, []string{"rules/common/testing.md"}, []string{tc.body})), "zip", "")
			if err != nil {
				t.Fatal(err)
			}
			if got := len(p.Members[0].ActiveContent()) > 0; got != tc.active {
				t.Fatalf("rule import approval for %q: %v", tc.body, p.Members[0].ActiveContent())
			}
		})
	}
}

func TestLegacySkillsNamedLikeComponentFoldersKeepTheirSupportFiles(t *testing.T) {
	for _, name := range []string{"agents", "commands", "rules"} {
		body := strings.Replace(harmless, "research", name, 1)
		p, err := skills.ImportArchive(t.Context(), bytes.NewReader(archive(t, []string{name + "/SKILL.md", name + "/reference.md"}, []string{body, "Plain support prose."})), "zip", "")
		if err != nil || len(p.Members) != 1 || p.Members[0].Kind != "" || p.Members[0].Name != name {
			t.Fatalf("legacy %s: %+v %v", name, p, err)
		}
	}
}

func TestRuleCompositionOrdersTargetsAndDeduplicatesIdenticalRevisions(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: componentAuthority{}})
	workspace := h.workspace(t, false)
	folder := skills.Target{Kind: "folder", ID: model.NewID().String()}
	for i, target := range []skills.Target{workspace, folder} {
		paths, bodies := []string{"rules/common/testing.md"}, []string{"SHARED RULE\n"}
		if i == 0 {
			paths = append(paths, "rules/common/z-base.md")
			bodies = append(bodies, "BROAD RULE\n")
		} else {
			paths = append(paths, "rules/common/a-project.md")
			bodies = append(bodies, "PROJECT RULE\n")
		}
		r := h.upload(t, target.Kind, target.Kind, "", archive(t, paths, bodies))
		var p skills.InstallResult
		if r.Code != http.StatusCreated || json.Unmarshal(r.Body.Bytes(), &p) != nil {
			t.Fatalf("install: %d %s", r.Code, r.Body.String())
		}
		if r := h.request("POST", "/v1/m/skills/assignments", skills.AssignmentRequest{Target: target, RevisionID: p.Revision.ID}, ""); r.Code != http.StatusCreated {
			t.Fatalf("assign: %d %s", r.Code, r.Body.String())
		}
	}
	selection, err := h.selection(workspace, folder)
	if err != nil {
		t.Fatal(err)
	}
	err = h.store.View(t.Context(), h.tenant, func(sc store.Scope) error {
		files, err := h.module.ReadSelectionFiles(t.Context(), sc, selection)
		if err != nil {
			return err
		}
		if len(files) != 1 || files[0].Kind != "rule" || files[0].Path != "selection.md" {
			t.Fatalf("native composition: %+v", files)
		}
		text := string(files[0].Bytes)
		if strings.Count(text, "SHARED RULE") != 1 || strings.Index(text, "BROAD RULE") > strings.Index(text, "PROJECT RULE") {
			t.Fatalf("target order/dedup: %s", text)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeMixedLayoutPreservesLegacySkillsWrapperManifest(t *testing.T) {
	plain, err := skills.ImportArchive(t.Context(), bytes.NewReader(archive(t, []string{"research/SKILL.md"}, []string{harmless})), "zip", "")
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := skills.ImportArchive(t.Context(), bytes.NewReader(archive(t, []string{"pack/skills/research/SKILL.md"}, []string{harmless})), "zip", "")
	if err != nil || plain.ManifestDigest != wrapped.ManifestDigest {
		t.Fatalf("legacy skills wrapper changed manifest: %+v %v", wrapped, err)
	}
	mixed, err := skills.ImportArchive(t.Context(), bytes.NewReader(archive(t, []string{"skills/research/SKILL.md", "commands/review.md"}, []string{harmless, "Review changes."})), "zip", "")
	if err != nil || len(mixed.Members) != 2 {
		t.Fatalf("native mixed root: %+v %v", mixed, err)
	}
}

func TestPortableFileImportMetadataKeepsItsHistoricalMeaning(t *testing.T) {
	body := strings.Replace(harmless, "license: MIT", "license: MIT\nfile-import: legacy-description", 1)
	pack, err := skills.ImportArchive(t.Context(), bytes.NewReader(archive(t, []string{"research/SKILL.md"}, []string{body})), "zip", "")
	if err != nil {
		t.Fatal(err)
	}
	if active := pack.Members[0].ActiveContent(); len(active) != 0 {
		t.Fatalf("portable descriptive metadata became active: %v", active)
	}
}

func TestRulesWithFileImportsCarryTheirRefusalAcrossComposition(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: componentAuthority{}})
	r := h.upload(t, "import-rules", "import-rules", "", archive(t, []string{"rules/common/a.md", "rules/common/b.md"}, []string{"Read @b.md", "Referenced instructions."}))
	var pack skills.InstallResult
	if r.Code != http.StatusCreated || json.Unmarshal(r.Body.Bytes(), &pack) != nil {
		t.Fatalf("install: %d %s", r.Code, r.Body.String())
	}
	target := h.workspace(t, false)
	r = h.request("POST", "/v1/m/skills/assignments", skills.AssignmentRequest{Target: target, RevisionID: pack.Revision.ID, ApproveActiveContent: true}, "")
	if r.Code != http.StatusCreated {
		t.Fatalf("assign: %d %s", r.Code, r.Body.String())
	}
	selected, err := h.selection(target)
	if err != nil {
		t.Fatal(err)
	}
	err = h.store.View(t.Context(), h.tenant, func(sc store.Scope) error {
		files, err := h.module.ReadSelectionFiles(t.Context(), sc, selected)
		if err != nil {
			return err
		}
		if len(files) != 1 || !files[0].Imports {
			t.Fatalf("composition lost native import marker: %+v", files)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

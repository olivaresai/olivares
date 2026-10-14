// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/skills"
)

func TestWikiInitCreatesAuditedFilesAndPreservesExistingContent(t *testing.T) {
	m, st, tenant, _ := newRuntimeHarness(t)
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
	old := []byte("# Existing instructions\n")
	if err := os.WriteFile(filepath.Join(ws.RootPath, "AGENTS.md"), old, 0644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	m.UseWikiSkills(func(ctx context.Context, mc api.ModuleContext, target skills.Target) (skills.Assignment, error) {
		calls++
		if target.Kind != "folder" || target.ID != ws.WorkspaceRef || mc.Tenant != tenant {
			t.Fatalf("wrong assignment target: %+v", target)
		}
		return skills.Assignment{ID: "pin", Target: target}, nil
	})
	mc := api.ModuleContext{Tenant: tenant, Principal: auth.Principal{}}
	for i := 0; i < 2; i++ {
		out, err := m.initWiki(context.Background(), mc, ws.WorkspaceRef)
		if err != nil {
			t.Fatal(err)
		}
		if out.Assignment.ID != "pin" {
			t.Fatalf("missing assignment: %+v", out)
		}
	}
	if calls != 2 {
		t.Fatalf("assignment was not corroborated on retry: %d", calls)
	}
	body, _ := os.ReadFile(filepath.Join(ws.RootPath, "AGENTS.md"))
	if string(body) != string(old) {
		t.Fatal("overwrote existing AGENTS.md")
	}
	for _, name := range []string{"raw", "wiki"} {
		info, err := os.Stat(filepath.Join(ws.RootPath, name))
		if err != nil || !info.IsDir() {
			t.Fatalf("missing directory %s: %v", name, err)
		}
	}
	for _, name := range []string{"index.md", "log.md"} {
		body, err := os.ReadFile(filepath.Join(ws.RootPath, name))
		if err != nil || len(body) == 0 {
			t.Fatalf("missing file %s: %v", name, err)
		}
	}
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		writes := map[string]bool{}
		err := sc.Audit().(store.CanonicalWalker).WalkCanonical(context.Background(), 0, func(ev model.AuditEvent, raw string, _ []byte) error {
			if strings.HasPrefix(ev.Action, "sessions.workspace.") {
				var meta map[string]any
				if err := json.Unmarshal([]byte(raw), &meta); err != nil {
					return err
				}
				if p, ok := meta["path"].(string); ok {
					writes[ev.Action+":"+p] = true
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, op := range []string{"mkdir:raw", "mkdir:wiki", "write:index.md", "write:log.md"} {
			if !writes["sessions.workspace."+op] {
				t.Errorf("missing audit %s", op)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWikiInitRefusesReadOnlyAndEscapingFolders(t *testing.T) {
	for _, mode := range []string{"readonly", "symlink", "occupied"} {
		t.Run(mode, func(t *testing.T) {
			m, _, tenant, _ := newRuntimeHarness(t)
			params := CreateWorkspaceParams{}
			if mode == "readonly" {
				params.MountMode = mountRO
			}
			ws := mkWorkspace(t, m, tenant, params)
			if mode == "symlink" {
				if err := os.Symlink(t.TempDir(), filepath.Join(ws.RootPath, "wiki")); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "occupied" {
				if err := os.WriteFile(filepath.Join(ws.RootPath, "raw"), []byte("keep"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			m.UseWikiSkills(func(context.Context, api.ModuleContext, skills.Target) (skills.Assignment, error) {
				return skills.Assignment{}, nil
			})
			_, err := m.initWiki(context.Background(), api.ModuleContext{Tenant: tenant}, ws.WorkspaceRef)
			if err == nil {
				t.Fatal("unsafe initialization succeeded")
			}
			if _, err := os.Stat(filepath.Join(ws.RootPath, "index.md")); !os.IsNotExist(err) {
				t.Fatal("created files after refusal")
			}
		})
	}
}

func TestWikiInitFailsClosedWhenSkillsAreUnavailable(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t)
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
	_, err := m.initWiki(context.Background(), api.ModuleContext{Tenant: tenant}, ws.WorkspaceRef)
	if !isStatus(err, 503) {
		t.Fatalf("unwired init: %v", err)
	}
	for _, name := range []string{"raw", "wiki", "index.md", "log.md", "AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(ws.RootPath, name)); !os.IsNotExist(err) {
			t.Errorf("created %s on failure", name)
		}
	}
}

func TestWikiInitRefusesSymlinkedSchemaAndRestrictedSubpaths(t *testing.T) {
	for _, mode := range []string{"schema-symlink", "restricted"} {
		t.Run(mode, func(t *testing.T) {
			m, _, tenant, _ := newRuntimeHarness(t)
			p := CreateWorkspaceParams{}
			if mode == "restricted" {
				p.AllowSubpaths = []string{"wiki"}
			}
			ws := mkWorkspace(t, m, tenant, p)
			if mode == "schema-symlink" {
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), filepath.Join(ws.RootPath, "AGENTS.md")); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			m.UseWikiSkills(func(context.Context, api.ModuleContext, skills.Target) (skills.Assignment, error) {
				called = true
				return skills.Assignment{}, nil
			})
			if _, err := m.initWiki(context.Background(), api.ModuleContext{Tenant: tenant}, ws.WorkspaceRef); err == nil {
				t.Fatal("unsafe init succeeded")
			}
			if called {
				t.Fatal("assigned a pack before validating wiki paths")
			}
		})
	}
}

func TestWikiInitDoesNotOverwriteConcurrentCreator(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t)
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
	m.UseWikiSkills(func(context.Context, api.ModuleContext, skills.Target) (skills.Assignment, error) {
		for _, name := range []string{"AGENTS.md", "index.md", "log.md"} {
			if err := os.WriteFile(filepath.Join(ws.RootPath, name), []byte("Concurrent writer\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		return skills.Assignment{ID: "pin"}, nil
	})
	if _, err := m.initWiki(t.Context(), api.ModuleContext{Tenant: tenant}, ws.WorkspaceRef); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AGENTS.md", "index.md", "log.md"} {
		got, err := os.ReadFile(filepath.Join(ws.RootPath, name))
		if err != nil || string(got) != "Concurrent writer\n" {
			t.Fatalf("overwrote concurrent %s: %q %v", name, got, err)
		}
	}
}

type wikiAuditDeniedData struct{ api.ModuleData }

func (d wikiAuditDeniedData) Mutate(context.Context, model.TenantID, func(store.Scope) error) error {
	return errors.New("injected wiki audit refusal")
}
func TestWikiInitAuditFailureRefusesFilesystemMutation(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t)
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
	called := false
	m.UseWikiSkills(func(context.Context, api.ModuleContext, skills.Target) (skills.Assignment, error) {
		called = true
		return skills.Assignment{ID: "pin"}, nil
	})
	m.UseData(wikiAuditDeniedData{m.Data})
	if _, err := m.initWiki(t.Context(), api.ModuleContext{Tenant: tenant}, ws.WorkspaceRef); !isStatus(err, 503) {
		t.Fatalf("audit refusal: %v", err)
	}
	if !called {
		t.Fatal("failed before the audited mutation was attempted")
	}
	for _, name := range []string{"raw", "wiki", "AGENTS.md", "index.md", "log.md"} {
		if _, err := os.Stat(filepath.Join(ws.RootPath, name)); !os.IsNotExist(err) {
			t.Errorf("mutated %s without audit", name)
		}
	}
}

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
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/confine"
	"github.com/olivaresai/olivares/modules/skills"
)

type sessionSkillsFixture struct {
	selection skills.Selection
	files     []skills.DeliveryFile
	err       error
}

func (f sessionSkillsFixture) ResolveSessionSkills(context.Context, model.TenantID, string) (skills.Selection, error) {
	return f.selection, f.err
}
func (f sessionSkillsFixture) ReadSessionSkillFiles(context.Context, model.TenantID, skills.Selection) ([]skills.DeliveryFile, error) {
	return f.files, f.err
}

func TestRuntimeSkillsDeliveryIsNativeIsolatedAndAudited(t *testing.T) {
	for _, mode := range []string{"default", "plan"} {
		t.Run(mode, func(t *testing.T) {
			driver := providerDriverClaude
			runner := &fakeRunner{initSID: "native-skills"}
			m, st, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()), WithConfinement([]string{t.TempDir()}, true))
			m.UseExecutionEnvironmentRef(testEnvRef)
			profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: driver, ConfigHome: t.TempDir(), UserHome: t.TempDir(), DisplayName: "skills", AuthSource: AuthSourceAccountHome})
			selection := skills.Selection{Digest: "selection", Members: []skills.SelectedMember{{Name: "research", PackID: model.NewID().String(), RevisionID: model.NewID().String(), ManifestDigest: "pack-sha", ContentDigest: "skill-sha"}}}
			body := []byte("---\nname: research\ndescription: Review primary sources.\n---\nRead support.md.\n")
			if err := m.UseSessionSkills(sessionSkillsFixture{selection: selection, files: []skills.DeliveryFile{{Path: "research/SKILL.md", Bytes: body, Mode: 0444}, {Path: "research/support.md", Bytes: []byte("support"), Mode: 0444}}}, t.TempDir()); err != nil {
				t.Fatal(err)
			}
			// prepareSessionLaunch is the common create/resume/work launch seam.
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			// The module stores and audits the run through its public launch interface.
			run, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: mode, ProviderProfileRef: profile.Ref, Actor: "user:u1", ActorKind: model.ActorUser})
			if err != nil {
				t.Fatal(err)
			}
			spec := runner.lastSpec()
			home := ""
			for _, env := range spec.Env {
				if env.Name == "HOME" {
					home = env.Value
				}
				if env.Name == m.configHomeEnvForDriver(driver) && env.Value != profile.ConfigHome {
					t.Fatal("profile configuration home changed")
				}
			}
			if home == "" || home == profile.UserHome || pathsOverlap(home, spec.Dir) {
				t.Fatalf("unisolated skill home: %q", home)
			}
			location := filepath.Join(home, ".claude", "skills")
			got, err := os.ReadFile(filepath.Join(location, "research", "SKILL.md"))
			if err != nil || string(got) != string(body) {
				t.Fatalf("native SKILL.md: %q %v", got, err)
			}
			if got, err := os.ReadFile(filepath.Join(location, "research", "support.md")); err != nil || string(got) != "support" {
				t.Fatalf("support: %q %v", got, err)
			}
			if driver == providerDriverClaude && !slices.Contains(spec.Args, "--plugin-dir") {
				t.Fatal("Claude native plugin discovery was not wired")
			}
			if !spec.ConfinementRequired || !spec.ConfinementRequireTruncateProtection || len(spec.Confinement.Sealed) == 0 {
				t.Fatal("assigned skills are not sealed")
			}
			count := 0
			err = st.View(t.Context(), tenant, func(sc store.Scope) error {
				return sc.Audit().(store.CanonicalWalker).WalkCanonical(t.Context(), 0, func(ev model.AuditEvent, raw string, _ []byte) error {
					if ev.Action == "sessions.skills.delivered" {
						count++
						var meta struct {
							RunRef  string                  `json:"run_ref"`
							SHA     string                  `json:"sha256"`
							Members []skills.SelectedMember `json:"members"`
						}
						if err := json.Unmarshal([]byte(raw), &meta); err != nil || meta.RunRef != run.RunRef || meta.SHA != selection.Digest || len(meta.Members) != 1 || meta.Members[0].RevisionID != selection.Members[0].RevisionID || meta.Members[0].PackID != selection.Members[0].PackID || ev.TargetKind != runKind {
							t.Fatalf("wrong audit target: %+v", ev)
						}
					}
					return nil
				})
			})
			if err != nil || count != 1 {
				t.Fatalf("delivery audit count=%d err=%v", count, err)
			}
			t.Run("sealed files", func(t *testing.T) {
				if state := confine.Probe(); state.Mode != confine.ModeLandlock || state.ABI < 3 {
					t.Skip("host lacks truncate-protected Landlock")
				}
				probe := spec
				probe.Program = "/bin/sh"
				probe.Args = []string{"-c", `cat "$HOME/.claude/skills/research/support.md" || exit 10; if printf changed > "$HOME/.claude/skills/research/support.md"; then exit 11; fi; if rm "$HOME/.claude/skills/research/SKILL.md"; then exit 12; fi; if rm -rf "$HOME/.claude"; then exit 13; fi; if mv "$HOME/.claude" "$HOME/replaced"; then exit 14; fi; echo cache > "$HOME/.cache/native-cache" || exit 15; echo write-denied`}
				if mode == "plan" {
					probe.Args[1] += `; if printf changed > workspace-write; then exit 16; fi`
				}
				proc, err := NewProcRunner().Launch(t.Context(), probe)
				if err != nil {
					t.Fatal(err)
				}
				var output strings.Builder
				for f := range proc.Output() {
					output.Write(f.Data)
				}
				if exit, _ := proc.Wait(); exit != 0 || !strings.Contains(output.String(), "write-denied") {
					t.Fatalf("sealed native files: exit=%d %s", exit, output.String())
				}
			})
		})
	}
}

func TestRuntimeSkillsNoAssignmentPreservesHomeAndErrorsRefuseSpawn(t *testing.T) {
	for _, cause := range []error{nil, errors.New("catalog unavailable")} {
		runner := &fakeRunner{initSID: "no-skills"}
		m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()))
		ref := ensureRuntimeTestProfileRef(t, m, tenant)
		if err := m.UseSessionSkills(sessionSkillsFixture{err: cause}, t.TempDir()); err != nil {
			t.Fatal(err)
		}
		_, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, ProviderProfileRef: ref, Actor: "user:u1", ActorKind: model.ActorUser})
		if cause != nil {
			if err == nil || len(runner.specs) != 0 {
				t.Fatalf("catalog error spawned child: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(runner.lastSpec().Args, "--plugin-dir") {
			t.Fatal("unassigned launch received skills")
		}
		profile, err := m.GetProfile(t.Context(), tenant, ref)
		if err != nil {
			t.Fatal(err)
		}
		for _, env := range runner.lastSpec().Env {
			if env.Name == "HOME" && env.Value != profile.UserHome {
				t.Fatal("unassigned HOME changed")
			}
		}
	}
}

type droppedSkillAuditStore struct {
	store.Store
	dropped *bool
}
type droppedSkillAuditScope struct {
	store.Scope
	dropped *bool
}
type droppedSkillAudit struct {
	store.AuditLog
	dropped *bool
}

func (s droppedSkillAuditStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return s.Store.Mutate(ctx, tenant, func(sc store.Scope) error { return fn(droppedSkillAuditScope{sc, s.dropped}) })
}
func (s droppedSkillAuditScope) Audit() store.AuditLog {
	return droppedSkillAudit{s.Scope.Audit(), s.dropped}
}
func (a droppedSkillAudit) Append(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
	if draft.Action == "sessions.skills.delivered" {
		*a.dropped = true
		return model.AuditEvent{}, nil
	}
	return a.AuditLog.Append(ctx, draft)
}

func TestRuntimeSkillsDroppedAuditRefusesSpawnAndCleansHome(t *testing.T) {
	runner := &fakeRunner{initSID: "dropped-audit"}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()), WithConfinement([]string{t.TempDir()}, true))
	ref := ensureRuntimeTestProfileRef(t, m, tenant)
	dropped := false
	m.UseData(api.NewModuleData(droppedSkillAuditStore{st, &dropped}))
	root := t.TempDir()
	if err := m.UseSessionSkills(sessionSkillsFixture{selection: skills.Selection{Digest: "fixture", Members: []skills.SelectedMember{{Name: "research"}}}, files: []skills.DeliveryFile{{Path: "research/SKILL.md", Bytes: []byte("fixture"), Mode: 0444}}}, root); err != nil {
		t.Fatal(err)
	}
	_, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, ProviderProfileRef: ref, Actor: "user:u1", ActorKind: model.ActorUser})
	if !dropped || err == nil || !strings.Contains(err.Error(), "assigned skills delivery failed; the session was not started") || len(runner.specs) != 0 {
		t.Fatalf("dropped audit spawned child: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed delivery home retained: %v %v", entries, err)
	}
}

type beforeSessionSkillsRegistry struct{ store.ExtensionRegistry }

func (r beforeSessionSkillsRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == runKind {
		fields := make([]model.FieldSpec, 0, len(d.Fields))
		for _, field := range d.Fields {
			if field.Name != colRunSkillsSelection {
				fields = append(fields, field)
			}
		}
		d.Fields = fields
	}
	return r.ExtensionRegistry.Register(d)
}

type storedSessionSkillsFixture struct {
	st       store.Store
	sessions *Module
	catalog  *skills.Module
}

func (f storedSessionSkillsFixture) ResolveSessionSkills(ctx context.Context, tenant model.TenantID, ref string) (selection skills.Selection, err error) {
	err = f.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		selection, err = f.sessions.PinSessionSkillsSelection(ctx, sc, ref, f.catalog)
		return err
	})
	return
}
func (f storedSessionSkillsFixture) ReadSessionSkillFiles(ctx context.Context, tenant model.TenantID, selection skills.Selection) (files []skills.DeliveryFile, err error) {
	err = f.st.View(ctx, tenant, func(sc store.Scope) error { files, err = f.catalog.ReadSelectionFiles(ctx, sc, selection); return err })
	return
}

func TestSessionSkillsHistoricalUpgradeResumesWithoutInventingAssignments(t *testing.T) {
	for _, backend := range profileBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			catalog := skills.New(skills.Options{ArtifactRoot: t.TempDir()})
			old := New()
			m, st := openProfileModule(t, backend, func(reg store.ExtensionRegistry) error {
				if err := old.RegisterSchema(beforeSessionSkillsRegistry{reg}); err != nil {
					return err
				}
				return catalog.RegisterSchema(reg)
			})
			tenant := ensureTenant(t, st, "skill-upgrade")
			workspaceRoot := t.TempDir()
			WithSessionWorkspaceRoot(workspaceRoot)(m)
			WithRunner(&fakeRunner{initSID: "historical-skills"})(m)
			WithCredentialSource(staticCred())(m)
			run, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = m.stopRun(t.Context(), tenant, run.RunRef, "user:u1", model.ActorUser); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			current := New()
			m, st = openProfileModule(t, backend, func(reg store.ExtensionRegistry) error {
				if err := current.RegisterSchema(reg); err != nil {
					return err
				}
				return catalog.RegisterSchema(reg)
			})
			t.Cleanup(func() { _ = st.Close() })
			WithSessionWorkspaceRoot(workspaceRoot)(m)
			WithRunner(&fakeRunner{initSID: "historical-skills"})(m)
			WithCredentialSource(staticCred())(m)
			if err := m.UseSessionSkills(storedSessionSkillsFixture{st, m, catalog}, t.TempDir()); err != nil {
				t.Fatal(err)
			}
			if _, err := m.resumeRun(t.Context(), tenant, run.RunRef, "user:u1", model.ActorUser, ""); err != nil {
				t.Fatal(err)
			}
			row := runRecord(t, st, tenant, run.RunRef)
			var selection skills.Selection
			if err := json.Unmarshal([]byte(row.String(colRunSkillsSelection)), &selection); err != nil || selection.Digest == "" || len(selection.Members) != 0 {
				t.Fatalf("historical snapshot: %+v %v", selection, err)
			}
		})
	}
}

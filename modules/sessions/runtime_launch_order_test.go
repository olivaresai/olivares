// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

func TestCreateAndResumeApplyTemplateBeforeSessionPolicy(t *testing.T) {
	fr := &fakeRunner{initSID: "launch-order"}
	m, st, tenant, profile, _ := profiledHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	id := seedTemplate(t, m, tenant, "Launch order", tplBody{})
	p := CreateRunParams{ProviderProfileRef: profile.Ref, TemplateID: id, Actor: "user:u1", ActorKind: "user"}
	dto, err := m.createRun(ctx, tenant, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, p.Actor, p.ActorKind); err != nil {
		t.Fatal(err)
	}
	archiveTemplate(t, m, tenant, id)
	// A stored policy this runtime cannot honor must not hide the template's
	// refusal on resume when a new launch reports the template first.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerProfileKind)
		if err != nil {
			return err
		}
		rec, err := findProfileRec(ctx, sc, profile.Ref)
		if err != nil {
			return err
		}
		rec[colPPSessionPermissionMode] = "unsupported"
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, createErr := m.createRun(ctx, tenant, p)
	_, resumeErr := m.resumeRun(ctx, tenant, dto.RunRef, p.Actor, p.ActorKind, "")
	if createErr == nil || !strings.Contains(createErr.Error(), "template") {
		t.Fatalf("create must refuse the template: %v", createErr)
	}
	if resumeErr == nil || statusOf(resumeErr) != statusOf(createErr) || resumeErr.Error() != createErr.Error() {
		t.Errorf("create and resume disagree: create=%v (status %d), resume=%v (status %d)",
			createErr, statusOf(createErr), resumeErr, statusOf(resumeErr))
	}
	if launchCount(fr) != 1 || countRuns(t, st, tenant) != 1 {
		t.Fatal("a refused launch spawned a child or persisted a new run")
	}
	if got, err := m.getRun(ctx, tenant, dto.RunRef); err != nil || got.State != stateStopped {
		t.Fatalf("refused resume changed the stopped session: %+v, %v", got, err)
	}
}

func TestCreateAndResumeKeepEffectiveLaunchTerms(t *testing.T) {
	for _, tc := range []struct {
		name, templateMode, profileMode, launchMode, wantMode string
		withTemplate                                          bool
	}{
		{"profile default", "", "plan", "", "plan", false},
		{"named preset", "", "bypassPermissions", "acceptEdits", "acceptEdits", false},
		{"template overrides profile", "plan", "bypassPermissions", "", "plan", true},
		{"template narrows preset", "plan", "bypassPermissions", "acceptEdits", "plan", true},
		{"template without mode", "", "bypassPermissions", "", "default", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{initSID: "launch-terms"}
			m, _, tenant, profile, _ := profiledHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
			ctx := context.Background()
			tools := []string{"Read", "Grep"}
			if _, err := m.PatchProfile(ctx, tenant, profile.Ref, ProfilePatch{
				SessionPermissionMode: &tc.profileMode, SessionTools: &tools,
			}); err != nil {
				t.Fatal(err)
			}
			p := CreateRunParams{ProviderProfileRef: profile.Ref, PermissionMode: tc.launchMode, Actor: "user:u1", ActorKind: "user"}
			if tc.withTemplate {
				p.TemplateID = seedTemplate(t, m, tenant, "Launch terms", tplBody{Settings: &tplSettings{PermissionMode: tc.templateMode}})
			}
			dto, err := m.createRun(ctx, tenant, p)
			if err != nil {
				t.Fatal(err)
			}
			check := func(wantTools string) {
				t.Helper()
				args := fr.lastSpec().Args
				if mode, _ := argvValue(args, "--permission-mode"); mode != tc.wantMode || dto.PermissionMode != tc.wantMode {
					t.Errorf("mode: child=%q row=%q, want %q", mode, dto.PermissionMode, tc.wantMode)
				}
				if got, present := argvValue(args, "--tools"); !present || got != wantTools {
					t.Errorf("tool surface: present=%v value=%q, want %q", present, got, wantTools)
				}
			}
			check("Grep,Read")
			if _, err := m.stopRun(ctx, tenant, dto.RunRef, p.Actor, p.ActorKind); err != nil {
				t.Fatal(err)
			}
			dto, err = m.resumeRun(ctx, tenant, dto.RunRef, p.Actor, p.ActorKind, "")
			if err != nil {
				t.Fatal(err)
			}
			check("Grep,Read")
			if _, err := m.stopRun(ctx, tenant, dto.RunRef, p.Actor, p.ActorKind); err != nil {
				t.Fatal(err)
			}
			// Tightening the current profile still governs the next resume.
			tools = []string{}
			if _, err := m.PatchProfile(ctx, tenant, profile.Ref, ProfilePatch{SessionTools: &tools}); err != nil {
				t.Fatal(err)
			}
			dto, err = m.resumeRun(ctx, tenant, dto.RunRef, p.Actor, p.ActorKind, "")
			if err != nil {
				t.Fatal(err)
			}
			check("")
		})
	}
}

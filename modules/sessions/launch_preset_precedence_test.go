// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// PEP, 2026-10-01: with no template, a profile's declared session_permission_mode
// REPLACED the mode the person chose at launch: a profile "default" turned an
// explicit acceptEdits into default, and a wider profile mode could replace "ask"
// or "read only". TARGET §3: the person's preset at launch is the session's
// preset. A profile mode applies only when the launch names none (until the
// migration removes it), and a bound that forbids the chosen preset refuses the
// launch with a sentence; nothing is changed silently, in either direction.
//
// Both clients name the mode on the launch body: the CLI sends ask=default,
// edits-only=acceptEdits, read-only=plan (cmd_session.go permissionModes) and the
// console's New session sends its choice the same way (first-hour api.ts), so the
// cases below are what each of them sends.

func launchUnderProfileMode(t *testing.T, profileMode, launchMode string, admin bool) (runDTO, LaunchSpec, error) {
	t.Helper()
	fr := &fakeRunner{initSID: "sess-precedence"}
	m, _, tenant, a, _ := profiledHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	if _, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{SessionPermissionMode: &profileMode}); err != nil {
		t.Fatalf("declare the profile mode: %v", err)
	}
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		PermissionMode: launchMode, ProviderProfileRef: a.Ref,
		Actor: "user:u1", ActorKind: "user", MayRunUnrestricted: admin,
	})
	var spec LaunchSpec
	fr.mu.Lock()
	if len(fr.specs) > 0 {
		spec = fr.specs[len(fr.specs)-1]
	}
	fr.mu.Unlock()
	return dto, spec, err
}

func TestTheLaunchPresetWinsOverTheProfileMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, profile, launch, want string
	}{
		{"profile full, the person asks: ask", "bypassPermissions", "default", "default"},
		{"profile full, the person reads only: read only", "bypassPermissions", "plan", "plan"},
		{"profile default, the person edits: edits", "default", "acceptEdits", "acceptEdits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dto, spec, err := launchUnderProfileMode(t, tc.profile, tc.launch, true)
			if err != nil {
				t.Fatalf("createRun: %v", err)
			}
			if got, _ := argvValue(spec.Args, "--permission-mode"); got != tc.want || dto.PermissionMode != tc.want {
				t.Fatalf("session mode argv=%q record=%q, want the person's %q", got, dto.PermissionMode, tc.want)
			}
		})
	}
}

// A launch that names no mode still takes the profile's, until the migration.
func TestAProfileModeAppliesOnlyWhenTheLaunchNamesNone(t *testing.T) {
	t.Parallel()
	dto, spec, err := launchUnderProfileMode(t, "plan", "", false)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if got, _ := argvValue(spec.Args, "--permission-mode"); got != "plan" || dto.PermissionMode != "plan" {
		t.Fatalf("an unnamed launch ran %q (record %q), want the profile's plan", got, dto.PermissionMode)
	}
}

// The bound today is "full needs run administration". The chosen preset is
// refused, never narrowed, and the sentence says what the person may choose. A
// profile's "full" on a launch that named nothing meets the same bound.
func TestAPolicyThatForbidsTheChosenPresetRefusesWithItsSentence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, profile, launch string }{
		{"the person chose full", "default", "bypassPermissions"},
		{"the profile's full on an unnamed launch", "bypassPermissions", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, spec, err := launchUnderProfileMode(t, tc.profile, tc.launch, false)
			if statusOf(err) != http.StatusForbidden {
				t.Fatalf("launch = %v, want 403", err)
			}
			for _, allowed := range []string{"ask", "read only", "edits only", "edits and commands"} {
				if !strings.Contains(err.Error(), allowed) {
					t.Fatalf("the refusal %q does not name %q", err.Error(), allowed)
				}
			}
			if len(spec.Args) != 0 {
				t.Fatal("a refused preset reached the runner")
			}
		})
	}
}

// SR2 on FH 031 (P2): a template's mode composed AFTER the person's choice widened it
// (ask -> edits, read only -> full). The person's narrower preset stands; a template
// narrower than the choice still narrows it, as its approved terms always did.
func TestATemplateNeverWidensTheNamedPreset(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, chosen, template, want string }{
		{"ask under an edits template stays ask", "default", "acceptEdits", "default"},
		{"read only under a full template stays read only", "plan", "bypassPermissions", "plan"},
		{"edits under a read-only template is read only", "acceptEdits", "plan", "plan"},
		{"a launch that names none takes the template's", "", "acceptEdits", "acceptEdits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
			id := seedTemplate(t, m, tenant, "Preset template", tplBody{Settings: &tplSettings{PermissionMode: tc.template}})
			dto, err := createProfiledTestRun(t, m, context.Background(), tenant, CreateRunParams{
				PermissionMode: tc.chosen, TemplateID: id, Actor: "user:u1", ActorKind: "user", MayRunUnrestricted: true,
			})
			if err != nil {
				t.Fatalf("createRun: %v", err)
			}
			if mode, _ := argvValue(fr.lastSpec().Args, "--permission-mode"); mode != tc.want || dto.PermissionMode != tc.want {
				t.Fatalf("child %q row %q, want %q", mode, dto.PermissionMode, tc.want)
			}
		})
	}
}

// An allowlist template is enforced by its own mode, so a narrower named preset
// cannot keep the template's terms: the launch is refused with the sentence, and
// nothing starts.
func TestAnAllowlistTemplateRefusesANarrowerNamedPreset(t *testing.T) {
	t.Parallel()
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	id := seedTemplate(t, m, tenant, "Edits and commands", tplBody{
		Settings: &tplSettings{PermissionMode: permModeDontAsk},
		Policies: &tplPolicies{AllowedTools: []string{"Read", "Bash"}},
	})
	_, err := createProfiledTestRun(t, m, context.Background(), tenant, CreateRunParams{
		PermissionMode: "plan", TemplateID: id, Actor: "user:u1", ActorKind: "user",
	})
	if statusOf(err) != http.StatusForbidden || !strings.Contains(err.Error(), "choose edits and commands") {
		t.Fatalf("createRun = %v, want 403 naming edits and commands", err)
	}
	if launchCount(fr) != 0 {
		t.Fatal("a refused launch started a child")
	}
}

// SR2 on FH 031 (P1): a resume re-applies today's template, and full permissions are
// judged on that result. A template widened to full after the launch does not let a
// resume without run administration (or a launch that never ran full) become full.
func TestAResumeIntoATemplateWidenedToFullNeedsRunAdministration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fr := &fakeRunner{initSID: "sess-widened"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	id := seedTemplate(t, m, tenant, "Mutable", tplBody{Settings: &tplSettings{PermissionMode: "plan"}})
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{PermissionMode: "plan", TemplateID: id, Actor: "user:u1", ActorKind: "user"})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	stopped := func() {
		t.Helper()
		waitFor(t, "the session id", func() bool {
			rec, lerr := m.loadRun(ctx, tenant, dto.RunRef)
			return lerr == nil && rec.String(colClaudeSessionID) != ""
		})
		if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
			t.Fatalf("stopRun: %v", err)
		}
	}
	stopped()
	retermTemplate(t, m, tenant, id, tplBody{Settings: &tplSettings{PermissionMode: "bypassPermissions"}})
	before := launchCount(fr)
	for name, resume := range map[string]func() error{
		"a caller without run administration": func() error {
			_, err := m.resumeRunAsCaller(ctx, tenant, dto.RunRef, "user:u1", "user", "", callerAsks{})
			return err
		},
		"the launch's own authority (it never ran full)": func() error {
			_, err := m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", "user", "")
			return err
		},
	} {
		if err := resume(); statusOf(err) != http.StatusForbidden {
			t.Fatalf("%s resumed into full: %v, want 403", name, err)
		}
	}
	if launchCount(fr) != before {
		t.Fatal("a refused resume started a child")
	}
	// A run administrator may resume into the full template, as they may launch it.
	if _, err := m.resumeRunAsCaller(ctx, tenant, dto.RunRef, "user:admin", "user", "", answeredYes); err != nil {
		t.Fatalf("an administrator's resume: %v", err)
	}
	if mode, _ := argvValue(fr.lastSpec().Args, "--permission-mode"); mode != "bypassPermissions" {
		t.Fatalf("the administrator's resume ran %q", mode)
	}
}

// Root on FH 048: a queued launch that asked for full permissions is judged, when its
// approval arrives, on the launcher's CURRENT run administration, not on the authority
// it had when it queued. Demoted to editor in between: refused, declined, no child.
// Still an administrator: it runs full.
func TestAQueuedFullLaunchAsksTheLaunchersCurrentRunAdministration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		demote bool
	}{{"demoted to editor before the approval", true}, {"still an administrator", false}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			gate := &controlledLaunchApproval{}
			runner := &fakeRunner{initSID: "queued-full"}
			m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()), WithLaunchGate(gate))
			h := newHarness(t, m)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "queued-full")
			if r := h.doJSON("POST", "/v1/users", admin, map[string]any{"email": "queued-full@fh.invalid", "password": "synthetic-password1", "tenant": tenant.String(), "role": auth.RoleAdmin}, nil); r.code != http.StatusCreated {
				t.Fatalf("tenant admin = %d %s", r.code, r.raw)
			}
			login := h.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": "queued-full@fh.invalid", "password": "synthetic-password1"}, nil)
			token, _ := login.body["token"].(string)
			issuer := auth.NewAuthenticator(h.st, nil)
			user, err := issuer.Authenticate(ctx, token)
			if err != nil {
				t.Fatal(err)
			}
			m.QueuedCredentialCapture = issuer.BindQueuedCredential
			m.QueuedLaunchAuthorization = func(ctx context.Context, tenant model.TenantID, credential auth.QueuedCredential, runID string, workspace model.ID) (auth.Principal, error) {
				return issuer.RevalidateQueuedCredential(ctx, credential)
			}
			queued := h.doJSON("POST", "/v1/m/sessions/runs", token, map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, h.m, tenant), "name": "queued full launch", "permission_mode": permModeBypass}, tenantHdr(tenant))
			if queued.code != http.StatusAccepted || launchCount(runner) != 0 {
				t.Fatalf("queued = %d %s", queued.code, queued.raw)
			}
			ref, _ := queued.body["run_ref"].(string)
			if tc.demote {
				if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
					members, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{eq("user_id", user.UserID.String())}, Limit: 10})
					if err != nil {
						return err
					}
					for _, member := range members {
						if member.TargetTenantID == tenant {
							member.Role = auth.RoleEditor
							_, err = as.Memberships().Update(ctx, member)
							return err
						}
					}
					return errors.New("membership missing")
				}); err != nil {
					t.Fatal(err)
				}
			}
			gate.approved.Store(true)
			if !tc.demote {
				waitFor(t, "the approved launch", func() bool { return launchCount(runner) > 0 })
				if mode, _ := argvValue(runner.lastSpec().Args, "--permission-mode"); mode != permModeBypass {
					t.Fatalf("a launcher still an administrator ran %q, want full", mode)
				}
				return
			}
			var reason string
			waitFor(t, "the refused launch to settle", func() bool {
				r := h.do("GET", "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenant))
				reason, _ = r.body["reason"].(string)
				return r.body["state"] == stateDeclined
			})
			if launchCount(runner) != 0 {
				t.Fatal("a demoted launcher's queued full launch started a child")
			}
			if !strings.Contains(reason, "only an administrator can run a session with full permissions") {
				t.Fatalf("declined reason = %q, want the full-permissions sentence", reason)
			}
		})
	}
}

// Root on FH 048, #371: a principal confined to one workspace does not run full, even
// with run administration, and a live launch or resume gets the same answer as a
// queued launch restored at its approval. The live answer asked run administration
// only, so a confined administrator was full live and refused once queued. It asks
// the two answers directly: over HTTP the store refuses a confined caller's launch
// and resume earlier today (sessions.alias, sessions.run_event carry no lineage).
func TestALiveAndAQueuedFullLaunchGiveAConfinedAdministratorTheSameAnswer(t *testing.T) {
	ctx := t.Context()
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "confined-full")
	var workspace model.ID
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		id := model.NewID()
		ws, err := sc.Workspaces().Create(ctx, model.Workspace{Name: id.String(), Slug: id.String(), Status: model.StatusActive})
		workspace = ws.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewAuthenticator(h.st, nil)
	administrator := func(email string, workspace model.ID) auth.Principal {
		t.Helper()
		body := map[string]any{"email": email, "password": "synthetic-password1", "tenant": tenant.String(), "role": auth.RoleAdmin}
		if !workspace.IsZero() {
			body["workspace_id"] = workspace.String()
		}
		if r := h.doJSON("POST", "/v1/users", admin, body, nil); r.code != http.StatusCreated {
			t.Fatalf("administrator = %d %s", r.code, r.raw)
		}
		login := h.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "synthetic-password1"}, nil)
		token, _ := login.body["token"].(string)
		p, err := issuer.Authenticate(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		if _, confined := p.ConfinedWorkspaceIn(tenant); confined != !workspace.IsZero() {
			t.Fatalf("fixture confinement of %s = %t", email, confined)
		}
		return p
	}
	for _, tc := range []struct {
		name string
		p    auth.Principal
		want bool
	}{
		{"a tenant-wide administrator", administrator("tenant-wide@fh.invalid", ""), true},
		{"an administrator confined to one workspace", administrator("confined@fh.invalid", workspace), false},
	} {
		live := m.mayRunUnrestricted(ctx, api.ModuleContext{Principal: tc.p, Tenant: tenant})
		queued := m.principalMayRunUnrestricted(ctx, tenant, tc.p)
		if live != tc.want || queued != tc.want {
			t.Errorf("%s: live full = %t, queued full = %t, want both %t", tc.name, live, queued, tc.want)
		}
	}
}

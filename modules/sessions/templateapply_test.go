// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// templateapply_test.go proves the thing the /apply endpoint used to only claim: that a
// template named at launch RESTRICTS the child, that a term this runtime cannot keep
// refuses the launch instead of being dropped, and — the control that stops all of the
// above from being satisfied by a gate that simply says no to everything — that a launch
// with NO template comes out byte-for-byte as it did before.

// --- helpers ----------------------------------------------------------------

// seedTemplate writes one template row directly and returns its id.
func seedTemplate(t *testing.T, m *Module, tenant model.TenantID, name string, body tplBody) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(templateKind)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rec, err := repo.Create(ctx, model.Record{
			colTplName:        name,
			colTplDescription: "seeded by test",
			colTplAuthor:      "test",
			colTplBuiltin:     false,
			colTplBody:        string(raw),
		})
		if err != nil {
			return err
		}
		id = rec.String(model.ColID)
		return nil
	})
	if err != nil {
		t.Fatalf("seedTemplate(%q): %v", name, err)
	}
	return id
}

// archiveTemplate soft-deletes a template so a launch naming it must refuse.
func archiveTemplate(t *testing.T, m *Module, tenant model.TenantID, id string) {
	t.Helper()
	ctx := context.Background()
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(templateKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(id))
		if err != nil {
			return err
		}
		rec[colTplArchivedAt] = m.clock.Now().String()
		_, err = repo.Update(ctx, rec)
		return err
	})
	if err != nil {
		t.Fatalf("archiveTemplate: %v", err)
	}
}

// retermTemplate rewrites a stored template's body (used to prove a resume re-resolves
// the CURRENT terms rather than the ones the run was born with).
func retermTemplate(t *testing.T, m *Module, tenant model.TenantID, id string, body tplBody) {
	t.Helper()
	ctx := context.Background()
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(templateKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(id))
		if err != nil {
			return err
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rec[colTplBody] = string(raw)
		_, err = repo.Update(ctx, rec)
		return err
	})
	if err != nil {
		t.Fatalf("retermTemplate: %v", err)
	}
}

// argvFlag returns the value that follows flag in an argv, and whether it is present.
func argvFlag(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func statusOf(err error) int {
	var re *runErr
	if errors.As(err, &re) {
		return re.status
	}
	return 0
}

// manyTools builds a distinct tool allowlist of n entries.
func manyTools(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "Tool" + strconv.Itoa(i)
	}
	return out
}

// restrictive is the template shape the whole pack is about: a tool allowlist plus the
// only mode under which one confines anything.
func restrictive() tplBody {
	return tplBody{
		Settings: &tplSettings{PermissionMode: permModeDontAsk, Effort: "high"},
		Policies: &tplPolicies{AllowedTools: []string{"Read", "Bash"}, RecordIO: boolPtr(true)},
	}
}

// --- the merge, as a pure value --------------------------------------------

func TestTemplateTerms_NamesEveryTermItCannotKeep(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body tplBody
		want string // a substring of the reason
	}{
		{"pre-tool hooks", tplBody{Hooks: &tplHooks{PreTool: []tplHookEntry{{Command: "true"}}}}, "hooks"},
		{"pre-session hooks", tplBody{Hooks: &tplHooks{PreSession: []tplHookEntry{{Command: "true"}}}}, "hooks"},
		{"connectors", tplBody{Connectors: []string{"github"}}, "connectors"},
		{"unknown dlp mode", tplBody{Policies: &tplPolicies{DLPMode: "block"}}, "dlp_mode"},
		{"unknown permission mode", tplBody{Settings: &tplSettings{PermissionMode: "yolo"}}, "permission_mode"},
		{"unknown effort", tplBody{Settings: &tplSettings{Effort: "ludicrous"}}, "effort"},
		{"duration over the ceiling", tplBody{Policies: &tplPolicies{MaxSessionDurationMinutes: maxTemplateSessionMinutes + 1}}, "max_session_duration_minutes"},
		{"comma in a tool spec", tplBody{Policies: &tplPolicies{AllowedTools: []string{"Bash,Read"}}}, "comma"},
		{
			"instructions over the argv budget",
			tplBody{Settings: &tplSettings{CustomInstructions: strings.Repeat("x", maxTemplateInstructions+1)}},
			"custom_instructions",
		},
		{
			"more allow rules than the argv budget",
			tplBody{Policies: &tplPolicies{AllowedTools: manyTools(maxTemplateTools + 1)}},
			"allowed_tools",
		},
		{"NUL in a tool spec", tplBody{Policies: &tplPolicies{AllowedTools: []string{"Ba\x00sh"}}}, "NUL"},
		{"NUL in the instructions", tplBody{Settings: &tplSettings{CustomInstructions: "a\x00b"}}, "NUL"},
		{
			"an explicit record_io=false",
			tplBody{Policies: &tplPolicies{RecordIO: boolPtr(false)}},
			"record_io=false",
		},
		{
			"a negative duration",
			tplBody{Policies: &tplPolicies{MaxSessionDurationMinutes: -1}},
			"max_session_duration_minutes",
		},
		{
			"an allow-list of blanks",
			tplBody{Policies: &tplPolicies{AllowedTools: []string{"  ", ""}}},
			"ambiguous",
		},
		{
			"allowlist under a mode that cannot enforce it",
			tplBody{
				Settings: &tplSettings{PermissionMode: "acceptEdits"},
				Policies: &tplPolicies{AllowedTools: []string{"Read"}},
			},
			"allowed_tools with settings.permission_mode",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := templateTerms(tc.body).unenforceable
			if len(got) == 0 {
				t.Fatalf("term should have been named unenforceable, got none")
			}
			if !strings.Contains(strings.Join(got, "|"), tc.want) {
				t.Errorf("reason should name %q, got %v", tc.want, got)
			}
		})
	}
}

// TestBuiltinsAreLaunchableOrRefusedForAReasonWeWroteDown is the NO-FIRE direction, and
// it doubles as the ledger of what the refusals COST. Without it every "it refuses"
// assertion in this file is satisfied by a reducer that refuses everything — which would
// refuse every template in the product and still look green.
//
// A seeded built-in that refuses its own launch is a defect we ship: it is written into
// every tenant and its description is read as a security posture. Three of them do, and
// the reasons are named here so the price is visible in the test rather than discovered
// by an operator:
//
//   - Refactoring and Security Audit declare HOOKS, which the launch cannot provision.
//   - Secure Development and Security Audit declare a DLP posture the child never
//     traverses under native isolation.
//
// When either becomes enforceable, this table is what tells you to come back.
func TestBuiltinsAreLaunchableOrRefusedForAReasonWeWroteDown(t *testing.T) {
	t.Parallel()

	refusedFor := map[string][]string{
		"Refactoring":        {"hooks"},
		"Security Audit":     {"hooks", "dlp_mode"},
		"Secure Development": {"dlp_mode"},
	}
	launchable := 0
	for _, bt := range builtinTemplates {
		t.Run(bt.name, func(t *testing.T) {
			got := templateTerms(bt.body).unenforceable
			want, expected := refusedFor[bt.name]
			switch {
			case !expected && len(got) > 0:
				t.Errorf("seeded built-in is not launchable and nobody wrote down why: %v", got)
			case expected && len(got) == 0:
				t.Errorf("expected to be refused for %v, but it launched — update this table", want)
			case expected:
				joined := strings.Join(got, "|")
				for _, reason := range want {
					if !strings.Contains(joined, reason) {
						t.Errorf("refused, but not for %q: %v", reason, got)
					}
				}
			default:
				launchable++
			}
		})
	}
	// The direction that stops the whole file from being satisfied by a blanket refusal.
	if launchable < 4 {
		t.Errorf("only %d built-in(s) can launch; the reducer is refusing more than we recorded", launchable)
	}
}

func TestTemplateTerms_AllowlistPinsTheEnforcingMode(t *testing.T) {
	t.Parallel()

	// A template that names an allowlist and stays silent about the mode gets dontAsk,
	// because it is the only mode under which the allowlist denies what it omits.
	var p CreateRunParams
	templateTerms(tplBody{Policies: &tplPolicies{AllowedTools: []string{"Read"}}}).applyTo(&p)
	if p.PermissionMode != permModeDontAsk {
		t.Errorf("permission_mode = %q, want %q", p.PermissionMode, permModeDontAsk)
	}
	if len(p.AllowedTools) != 1 || p.AllowedTools[0] != "Read" {
		t.Errorf("allowed_tools = %v", p.AllowedTools)
	}
}

func TestApplyTo_ConflictsAreRealAndOnlyReal(t *testing.T) {
	t.Parallel()

	terms := templateTerms(restrictive())

	// The request contradicts the template on two fields it CHOSE.
	p := CreateRunParams{PermissionMode: "bypassPermissions", Effort: "low"}
	conflicts := terms.applyTo(&p)
	if len(conflicts) != 2 {
		t.Fatalf("conflicts = %d (%v), want 2", len(conflicts), conflicts)
	}
	if p.PermissionMode != permModeDontAsk || p.Effort != "high" {
		t.Errorf("the template must win: mode=%q effort=%q", p.PermissionMode, p.Effort)
	}

	// The request chose nothing: the template fills it in, and filling in a blank is
	// not a contradiction. This is what made the old `conflicts: []` a lie rather than
	// merely a simplification — it was constant in BOTH directions.
	blank := CreateRunParams{}
	if got := terms.applyTo(&blank); len(got) != 0 {
		t.Errorf("an unspecified field is not a conflict, got %v", got)
	}
	if blank.PermissionMode != permModeDontAsk {
		t.Errorf("mode = %q", blank.PermissionMode)
	}
}

func TestApplyTo_RecordingOnlyEverGoesOn(t *testing.T) {
	t.Parallel()

	// A template cannot switch evidence OFF: the launch gate ORs its CRITICAL floor over
	// this, so honoring `false` would be a promise the gate is free to break — and a
	// template that could disable recording would launder a privileged launch.
	p := CreateRunParams{RecordRequested: true}
	templateTerms(tplBody{Policies: &tplPolicies{RecordIO: boolPtr(false)}}).applyTo(&p)
	if !p.RecordRequested {
		t.Error("a template must not be able to turn I/O recording off")
	}
}

// TestTemplateTerms_DLPIsRefusedRatherThanPresentedAsGovernance replaces a check that
// asserted the WRONG design. The first version treated the template's dlp_mode as a
// precondition on the workspace's declared posture and let the launch through — but a
// native child gets the workspace directory as its cwd and never traverses the classifier,
// so the session was presented as DLP-governed while its reads bypassed the DLP.
func TestTemplateTerms_DLPIsRefusedRatherThanPresentedAsGovernance(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{dlpLabel, dlpDeny} {
		t.Run(mode, func(t *testing.T) {
			got := templateTerms(tplBody{Policies: &tplPolicies{DLPMode: mode}}).unenforceable
			if len(got) == 0 {
				t.Fatal("a DLP posture the child never traverses must refuse, not pass as governance")
			}
			if !strings.Contains(strings.Join(got, "|"), "dlp_mode") {
				t.Errorf("the refusal must name the field: %v", got)
			}
		})
	}
	// No-fire direction: `off` demands nothing, so it is not refused — otherwise the test
	// above is satisfied by refusing every template that mentions DLP at all.
	if got := templateTerms(tplBody{Policies: &tplPolicies{DLPMode: dlpOff}}).unenforceable; len(got) > 0 {
		t.Errorf("dlp_mode=off demands nothing and must not refuse: %v", got)
	}
}

func TestUnenforceableForTransport_RecordingNeedsBridgedIO(t *testing.T) {
	t.Parallel()

	terms := templateTerms(tplBody{Policies: &tplPolicies{RecordIO: boolPtr(true)}})
	if got := unenforceableForTransport(terms, TransportRemoteControl); len(got) == 0 {
		t.Error("remote-control bridges no I/O, so a mandated recording has nothing to anchor and must refuse")
	}
	// No-fire: the transport that DOES bridge its I/O is not refused.
	if got := unenforceableForTransport(terms, TransportStreamJSON); len(got) > 0 {
		t.Errorf("stream-json bridges its I/O and must not be refused: %v", got)
	}
	// And a template that never asked for recording is not refused on any transport.
	quiet := templateTerms(tplBody{Settings: &tplSettings{Effort: "high"}})
	if got := unenforceableForTransport(quiet, TransportRemoteControl); len(got) > 0 {
		t.Errorf("a template that mandates no recording must not be refused: %v", got)
	}
}

// --- the launch -------------------------------------------------------------

// TestCreateRun_TemplateRestrictsTheChild is the headline. It asserts what the child is
// actually spawned with, which is the only place a "restriction" is one.
func TestCreateRun_TemplateRestrictsTheChild(t *testing.T) {
	t.Parallel()

	r := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(r), WithCredentialSource(staticCred()))
	id := seedTemplate(t, m, tenant, "Locked Down", restrictive())

	dto, err := m.createRun(context.Background(), tenant, CreateRunParams{
		TemplateID: id, Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}

	args := r.lastSpec().Args
	tools, ok := argvFlag(args, "--allowedTools")
	if !ok {
		t.Fatalf("the child was launched with no tool allowlist: %v", args)
	}
	if tools != "Read,Bash" {
		t.Errorf("--allowedTools = %q, want %q", tools, "Read,Bash")
	}
	mode, _ := argvFlag(args, "--permission-mode")
	if mode != permModeDontAsk {
		// Without dontAsk the allowlist only AUTO-APPROVES; it confines nothing. Emitting
		// one without the other is a widening that reads as a lock-down.
		t.Errorf("--permission-mode = %q, want %q (an allowlist confines nothing in any other mode)", mode, permModeDontAsk)
	}
	if effort, _ := argvFlag(args, "--effort"); effort != "high" {
		t.Errorf("--effort = %q, want high", effort)
	}
	if dto.TemplateID != id {
		t.Errorf("run DTO template_id = %q, want %q", dto.TemplateID, id)
	}
	if !dto.RecordIO {
		t.Error("the template mandates I/O recording and the run does not carry it")
	}
}

// TestCreateRun_WithoutATemplateIsUntouched is the OTHER no-fire direction, and the one
// the brief names: this pack must not move a single existing launch. Every flag the
// template plane can emit must be absent when no template is named.
func TestCreateRun_WithoutATemplateIsUntouched(t *testing.T) {
	t.Parallel()

	r := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(r), WithCredentialSource(staticCred()))

	dto, err := m.createRun(context.Background(), tenant, CreateRunParams{
		PermissionMode: "plan", Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	args := r.lastSpec().Args
	for _, flag := range []string{"--allowedTools", "--append-system-prompt"} {
		if _, present := argvFlag(args, flag); present {
			t.Errorf("%s reached an untemplated launch: %v", flag, args)
		}
	}
	if mode, _ := argvFlag(args, "--permission-mode"); mode != "plan" {
		t.Errorf("--permission-mode = %q, want the caller's own %q", mode, "plan")
	}
	if dto.TemplateID != "" {
		t.Errorf("template_id = %q, want empty", dto.TemplateID)
	}
	if lr, ok := m.rt.getLive(tenant, dto.RunRef); ok {
		lr.mu.Lock()
		armed := lr.deadline != nil
		lr.mu.Unlock()
		if armed {
			t.Error("an untemplated run must carry no duration ceiling")
		}
	}
}

func TestCreateRun_RefusesATemplateItCannotKeep(t *testing.T) {
	t.Parallel()

	r := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(r), WithCredentialSource(staticCred()))
	id := seedTemplate(t, m, tenant, "Hooked", tplBody{
		Hooks:    &tplHooks{PreTool: []tplHookEntry{{Command: "echo hi"}}},
		Policies: &tplPolicies{AllowedTools: []string{"Read"}},
	})

	_, err := m.createRun(context.Background(), tenant, CreateRunParams{
		TemplateID: id, Actor: "user:u1", ActorKind: "user",
	})
	if err == nil {
		t.Fatal("a template whose hooks cannot be provisioned must refuse the launch, not start without them")
	}
	if statusOf(err) != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", statusOf(err))
	}
	if !strings.Contains(err.Error(), "hooks") {
		t.Errorf("the refusal must name the field: %v", err)
	}
	// And nothing was launched: a refusal that still spawns the child is worse than none.
	r.mu.Lock()
	spawned := len(r.procs)
	r.mu.Unlock()
	if spawned != 0 {
		t.Errorf("%d processes spawned by a refused launch", spawned)
	}
}

func TestCreateRun_RefusesAnArchivedTemplate(t *testing.T) {
	t.Parallel()

	r := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(r), WithCredentialSource(staticCred()))
	id := seedTemplate(t, m, tenant, "Retired", restrictive())

	// It launches while live — the no-fire control for the refusal below.
	if _, err := m.createRun(context.Background(), tenant, CreateRunParams{
		TemplateID: id, Actor: "user:u1", ActorKind: "user",
	}); err != nil {
		t.Fatalf("a live template must launch: %v", err)
	}

	archiveTemplate(t, m, tenant, id)
	_, err := m.createRun(context.Background(), tenant, CreateRunParams{
		TemplateID: id, Actor: "user:u1", ActorKind: "user",
	})
	if err == nil || statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("an archived template must not keep governing launches: err=%v status=%d", err, statusOf(err))
	}
}

// TestCreateRun_RefusesADLPTermTheChildWouldBypass replaces a test that asserted the
// wrong design: it used to launch a session whose workspace METADATA met the template's
// DLP floor, which reported a governance posture the child's own reads never went through.
func TestCreateRun_RefusesADLPTermTheChildWouldBypass(t *testing.T) {
	t.Parallel()

	r := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(r), WithCredentialSource(staticCred()))
	id := seedTemplate(t, m, tenant, "Strict DLP", tplBody{Policies: &tplPolicies{DLPMode: dlpDeny}})
	ws, cerr := m.createWorkspace(context.Background(), tenant, CreateWorkspaceParams{
		RootPath: t.TempDir(), DLPMode: dlpDeny, Actor: "user:u1", ActorKind: "user",
	})
	if cerr != nil {
		t.Fatalf("createWorkspace: %v", cerr)
	}
	// Even against a workspace that DECLARES the strictest posture: the metadata matching
	// is not the enforcement, and the launch says so instead of passing.
	_, err := m.createRun(context.Background(), tenant, CreateRunParams{
		TemplateID: id, WorkspaceRef: ws.WorkspaceRef, Actor: "user:u1", ActorKind: "user",
	})
	if err == nil || statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("a DLP term the child bypasses must refuse: err=%v status=%d", err, statusOf(err))
	}
	// No-fire: the same launch without the DLP term goes through, so the refusal is about
	// the term and not about templates-with-workspaces in general.
	plain := seedTemplate(t, m, tenant, "No DLP", restrictive())
	if _, err := m.createRun(context.Background(), tenant, CreateRunParams{
		TemplateID: plain, WorkspaceRef: ws.WorkspaceRef, Actor: "user:u1", ActorKind: "user",
	}); err != nil {
		t.Fatalf("a template without a DLP term must launch: %v", err)
	}
}

// TestCreateRun_RefusesRecordingOnATransportThatBridgesNothing: a remote-control session
// relays its I/O to the provider, so a mandated recording would anchor an empty chain
// while the run row said "recorded".
func TestCreateRun_RefusesRecordingOnATransportThatBridgesNothing(t *testing.T) {
	t.Parallel()

	r := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(r), WithCredentialSource(staticCred()))
	id := seedTemplate(t, m, tenant, "Recorded", tplBody{Policies: &tplPolicies{RecordIO: boolPtr(true)}})

	_, err := m.createRun(context.Background(), tenant, CreateRunParams{
		TemplateID: id, Transport: TransportRemoteControl, Actor: "user:u1", ActorKind: "user",
	})
	if err == nil || statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("recording on a transport with no bridged I/O must refuse: err=%v status=%d", err, statusOf(err))
	}
	// No-fire: the same template on the transport that DOES bridge its I/O launches and
	// the run really does carry the recording flag.
	dto, err := m.createRun(context.Background(), tenant, CreateRunParams{
		TemplateID: id, Transport: TransportStreamJSON, Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("stream-json bridges its I/O and must launch: %v", err)
	}
	if !dto.RecordIO {
		t.Error("the run does not carry the recording the template mandates")
	}
}

// TestCreateRun_PersistsTheTemplateRevisionItRanUnder: a template is MUTABLE, so its id
// alone does not say what a running child was started under.
func TestCreateRun_PersistsTheTemplateRevisionItRanUnder(t *testing.T) {
	t.Parallel()

	r := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(r), WithCredentialSource(staticCred()))
	id := seedTemplate(t, m, tenant, "Versioned", restrictive())

	dto, err := m.createRun(context.Background(), tenant, CreateRunParams{
		TemplateID: id, Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if dto.TemplateVersion == 0 {
		t.Fatal("the run does not record which revision of the template it ran under")
	}
	first := dto.TemplateVersion

	// Edit the template and relaunch: the new run must record the NEW revision.
	retermTemplate(t, m, tenant, id, tplBody{
		Settings: &tplSettings{PermissionMode: permModeDontAsk},
		Policies: &tplPolicies{AllowedTools: []string{"Read"}},
	})
	next, err := m.createRun(context.Background(), tenant, CreateRunParams{
		TemplateID: id, Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun after the edit: %v", err)
	}
	if next.TemplateVersion == first {
		t.Errorf("both runs record revision %d; an edited template is indistinguishable from the one approved", first)
	}
}

// TestExpireRun_DoesNotTouchASuccessorRun: Timer.Stop does not wait for a callback that
// has already started, so an expiry in flight while the run is resumed must recognize that
// a NEW handle owns the run and do nothing.
func TestExpireRun_DoesNotTouchASuccessorRun(t *testing.T) {
	t.Parallel()

	r := &fakeRunner{initSID: "sess-succ"}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(r), WithCredentialSource(staticCred()))
	timer := &fakeTimer{}
	m.rt.afterFunc = func(d time.Duration, f func()) runTimer {
		timer.mu.Lock()
		timer.d, timer.fire = d, f
		timer.mu.Unlock()
		return timer
	}
	id := seedTemplate(t, m, tenant, "Bounded", tplBody{Policies: &tplPolicies{MaxSessionDurationMinutes: 60}})
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, CreateRunParams{TemplateID: id, Actor: "user:u1", ActorKind: "user"})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	first, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("no live handle")
	}
	waitFor(t, "the session id to be captured", func() bool {
		rec, lerr := m.loadRun(ctx, tenant, dto.RunRef)
		return lerr == nil && rec.String(colClaudeSessionID) != ""
	})
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	if _, err := m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", "user", ""); err != nil {
		t.Fatalf("resumeRun: %v", err)
	}
	before := len(listRunEvents(t, st, tenant, dto.RunRef))

	// The OLD handle's ceiling fires late. It must recognize the successor and stand down.
	m.expireRun(first, time.Hour)

	if got, gerr := m.getRun(ctx, tenant, dto.RunRef); gerr != nil || got.State != stateRunning {
		t.Fatalf("the successor was stopped by its predecessor's ceiling: state=%v err=%v", got.State, gerr)
	}
	if after := len(listRunEvents(t, st, tenant, dto.RunRef)); after != before {
		t.Errorf("the stale ceiling wrote %d event(s) onto the successor's ledger", after-before)
	}
}

// TestResumeRun_ReResolvesTheCurrentTerms: the posture a session runs under is the
// CURRENT one. A template tightened between launch and resume governs the relaunch.
func TestResumeRun_ReResolvesTheCurrentTerms(t *testing.T) {
	t.Parallel()

	r := &fakeRunner{initSID: "sess-tpl"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(r), WithCredentialSource(staticCred()))
	id := seedTemplate(t, m, tenant, "Tightening", tplBody{
		Settings: &tplSettings{PermissionMode: permModeDontAsk},
		Policies: &tplPolicies{AllowedTools: []string{"Read", "Bash"}},
	})
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, CreateRunParams{TemplateID: id, Actor: "user:u1", ActorKind: "user"})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "the session id to be captured", func() bool {
		rec, lerr := m.loadRun(ctx, tenant, dto.RunRef)
		return lerr == nil && rec.String(colClaudeSessionID) != ""
	})
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}

	// Bash is withdrawn while the session is stopped.
	retermTemplate(t, m, tenant, id, tplBody{
		Settings: &tplSettings{PermissionMode: permModeDontAsk},
		Policies: &tplPolicies{AllowedTools: []string{"Read"}},
	})
	if _, err := m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", "user", ""); err != nil {
		t.Fatalf("resumeRun: %v", err)
	}
	tools, _ := argvFlag(r.lastSpec().Args, "--allowedTools")
	if tools != "Read" {
		t.Errorf("the resume ran under the OLD terms: --allowedTools = %q, want %q", tools, "Read")
	}
}

func TestResumeRun_RefusesATemplateThatBecameUnenforceable(t *testing.T) {
	t.Parallel()

	r := &fakeRunner{initSID: "sess-tpl-2"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(r), WithCredentialSource(staticCred()))
	id := seedTemplate(t, m, tenant, "Will Break", restrictive())
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, CreateRunParams{TemplateID: id, Actor: "user:u1", ActorKind: "user"})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "the session id to be captured", func() bool {
		rec, lerr := m.loadRun(ctx, tenant, dto.RunRef)
		return lerr == nil && rec.String(colClaudeSessionID) != ""
	})
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	retermTemplate(t, m, tenant, id, tplBody{Hooks: &tplHooks{PreTool: []tplHookEntry{{Command: "x"}}}})

	_, err = m.resumeRun(ctx, tenant, dto.RunRef, "user:u1", "user", "")
	if err == nil || statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("a resume under terms this runtime can no longer keep must refuse: err=%v status=%d", err, statusOf(err))
	}
}

// --- the duration ceiling ---------------------------------------------------

// fakeTimer is the controllable one-shot behind the duration ceiling. The timer is a
// seam of its own because a fake CLOCK with a real time.AfterFunc measures the host:
// the test would advance an hour and then wait an hour.
type fakeTimer struct {
	mu      sync.Mutex
	fire    func()
	stopped bool
	d       time.Duration
}

func (f *fakeTimer) Stop() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return true
}

func (f *fakeTimer) trigger() {
	f.mu.Lock()
	fn, stopped := f.fire, f.stopped
	f.mu.Unlock()
	if fn != nil && !stopped {
		fn()
	}
}

func TestRunDeadline_EndsTheSessionAtTheTemplateCeiling(t *testing.T) {
	t.Parallel()

	r := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(r), WithCredentialSource(staticCred()))
	timer := &fakeTimer{}
	m.rt.afterFunc = func(d time.Duration, f func()) runTimer {
		timer.mu.Lock()
		timer.d, timer.fire = d, f
		timer.mu.Unlock()
		return timer
	}
	id := seedTemplate(t, m, tenant, "Bounded", tplBody{
		Policies: &tplPolicies{MaxSessionDurationMinutes: 60},
	})
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, CreateRunParams{TemplateID: id, Actor: "user:u1", ActorKind: "user"})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	timer.mu.Lock()
	armed := timer.d
	timer.mu.Unlock()
	if armed != time.Hour {
		t.Fatalf("ceiling armed at %v, want 1h", armed)
	}

	timer.trigger()
	waitFor(t, "the session to be stopped by its ceiling", func() bool {
		got, gerr := m.getRun(ctx, tenant, dto.RunRef)
		return gerr == nil && got.State == stateStopped
	})
	// It is recorded as STOPPED with its reason, never misfiled as FAILED: a session that
	// reached its ceiling did what it was told.
	var sawWhy bool
	for _, ev := range listRunEvents(t, st, tenant, dto.RunRef) {
		if strings.Contains(ev.Detail, "max session duration reached") {
			sawWhy = true
		}
		if ev.Event == "failed" {
			t.Errorf("the ceiling misfiled the stop as a failure: %+v", ev)
		}
	}
	if !sawWhy {
		t.Error("the ledger does not say WHY the session ended")
	}
}

func TestRunDeadline_IsReleasedWhenTheSessionEndsFirst(t *testing.T) {
	t.Parallel()

	r := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(r), WithCredentialSource(staticCred()))
	timer := &fakeTimer{}
	m.rt.afterFunc = func(d time.Duration, f func()) runTimer {
		timer.mu.Lock()
		timer.d, timer.fire = d, f
		timer.mu.Unlock()
		return timer
	}
	id := seedTemplate(t, m, tenant, "Bounded", tplBody{Policies: &tplPolicies{MaxSessionDurationMinutes: 60}})
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, CreateRunParams{TemplateID: id, Actor: "user:u1", ActorKind: "user"})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	waitFor(t, "the ceiling to be released", func() bool {
		timer.mu.Lock()
		defer timer.mu.Unlock()
		return timer.stopped
	})
}

// --- the /apply preview -----------------------------------------------------

func TestApplyPreview_MatchesTheLaunch(t *testing.T) {
	t.Parallel()

	// The preview and the launch must not be two implementations that happen to agree.
	// This asserts they are the same reduction and the same merge.
	terms := templateTerms(restrictive())
	target := applyTarget{PermissionMode: "bypassPermissions"}
	p := target.toParams()
	conflicts := terms.applyTo(&p)
	merged := targetOf(p)

	if len(conflicts) != 1 || conflicts[0].Field != "permission_mode" {
		t.Fatalf("conflicts = %v", conflicts)
	}
	if merged.PermissionMode != permModeDontAsk {
		t.Errorf("merged mode = %q", merged.PermissionMode)
	}
	if strings.Join(merged.AllowedTools, ",") != "Read,Bash" {
		t.Errorf("merged tools = %v", merged.AllowedTools)
	}
	if !merged.RecordIO {
		t.Error("merged record_io should be on")
	}
}

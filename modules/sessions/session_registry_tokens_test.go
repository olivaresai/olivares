// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// A session that ran under a profile with a package registry token printed what
// its tool printed: the tool's own transcript, which the engine reads back
// unredacted, may hold the token. Only a live session has the redactor that
// withholds it, so a session that is not live is not read (the same rule as for a
// session given vault secrets), while a live one still answers, redacted.

// registryTranscriptFixture is a stopped session whose transcript, in a message,
// a background command and a subagent's prompt, quotes a registry token, as a
// person debugging a 401 from .npmrc would.
func registryTranscriptFixture(t *testing.T, opts ...Option) *moveFixture {
	t.Helper()
	f := newMoveFixture(t, opts...)
	registryTranscriptQuoting(t, f)
	return f
}

// registryTranscriptQuoting is the transcript of f's session, quoting the token.
func registryTranscriptQuoting(t *testing.T, f *moveFixture) {
	t.Helper()
	transcript := activityFixtureHome(t, f)
	head, err := os.ReadFile(transcript)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, transcript, string(head)+
		`{"type":"user","isSidechain":false,"message":{"role":"user","content":"npm answers 401 with `+registryCanary+`"}}`+"\n"+
		`{"type":"assistant","isSidechain":false,"message":{"content":[{"type":"tool_use","id":"toolu_npm1","name":"Bash","input":{"command":"npm whoami //npm.acme.example/:_authToken=`+registryCanary+`","description":"Check `+registryCanary+`","run_in_background":true}}]}}`+"\n")
	writeFile(t, filepath.Join(filepath.Dir(transcript), moveTestSID, "subagents", "agent-a1.jsonl"),
		`{"type":"user","isSidechain":true,"agentId":"a1","message":{"role":"user","content":"Use `+registryCanary+`"}}`+"\n"+
			`{"type":"assistant","isSidechain":true,"agentId":"a1","message":{"content":[{"type":"text","text":"done"}]}}`+"\n")
}

func (f *moveFixture) declareRegistries(t *testing.T, registries []SessionRegistry) {
	t.Helper()
	programs := []string{}
	if _, err := f.m.PatchProfile(context.Background(), f.tenant, f.a, ProfilePatch{
		SessionRegistries: &registries, SessionPrograms: &programs, MayUseSecretEnv: true,
	}); err != nil {
		t.Fatal(err)
	}
}

// transcriptReads are the reads of a session's stored conversation.
func (f *moveFixture) transcriptReads() [][2]string {
	base := "/v1/m/sessions/runs/" + f.runRef
	return [][2]string{
		{"activity", base + "/activity"},
		{"activity/agents", base + "/activity/agents/a1"},
		{"handoff preview", base + "/handoff"},
	}
}

// refusedAsRegistryBearing checks every read and every act that carries the
// conversation elsewhere: 409, no token, nothing written.
func (f *moveFixture) refusedAsRegistryBearing(t *testing.T) {
	t.Helper()
	for _, read := range f.transcriptReads() {
		r := f.h.doJSON("GET", read[1], f.admin, nil, f.hdr)
		if r.code != http.StatusConflict || strings.Contains(r.raw, registryCanary) ||
			!strings.Contains(r.raw, "package registry token") {
			t.Fatalf("not live, %s = %d %s, want 409 naming the package registry token", read[0], r.code, r.raw)
		}
	}
	runs, events := f.runCount(t), len(f.events(t))
	for _, action := range []string{"handoff", "move"} {
		r := f.post(t, action, map[string]any{"provider_profile_ref": f.b})
		if r.code != http.StatusConflict || strings.Contains(r.raw, registryCanary) {
			t.Fatalf("not live, %s = %d %s, want 409", action, r.code, r.raw)
		}
	}
	noStateIn(t, f.homeB)
	if f.runCount(t) != runs || len(f.events(t)) != events {
		t.Fatal("a refused act started a session or wrote an event")
	}
}

func TestRegistryTokens_ATranscriptReadOfASessionThatIsNotLiveIsRefused(t *testing.T) {
	vault := newFakeVault()
	f := registryTranscriptFixture(t, WithProviderSecretVault(vault), WithClaudeHookPEP(t.TempDir(), "/bin/true"))
	vault.values[f.tenant.String()+"|"+acmeRegistry.Secret] = registryCanary

	// A profile that declares the registry after the session was created: the
	// resume is the launch that opens the token.
	f.stop(t)
	f.declareRegistries(t, []SessionRegistry{acmeRegistry})
	if r := f.post(t, "resume", nil); r.code != http.StatusOK {
		t.Fatalf("resume = %d %s", r.code, r.raw)
	}

	// Live: the redactor of the running session withholds the token; nothing changes.
	for _, read := range f.transcriptReads()[:2] {
		r := f.h.doJSON("GET", read[1], f.admin, nil, f.hdr)
		if r.code != http.StatusOK || strings.Contains(r.raw, registryCanary) {
			t.Fatalf("live %s = %d %s, want 200 without the token", read[0], r.code, r.raw)
		}
	}
	if r := f.h.doJSON("GET", f.transcriptReads()[0][1], f.admin, nil, f.hdr); !strings.Contains(r.raw, "[secret "+acmeRegistry.Secret+"]") {
		t.Fatalf("a live session's activity does not name the withheld secret: %s", r.raw)
	}

	// Not live: a stopped session keeps its redactor in this engine until its handle
	// is released or the engine restarts, so the restart is what this models.
	f.stop(t)
	f.m.rt.dropLive(f.tenant, f.runRef)
	f.refusedAsRegistryBearing(t)
}

// A session created under a profile that already declares the registry is
// recorded at creation; no resume is needed to refuse it after a restart.
func TestRegistryTokens_ASessionCreatedWithATokenIsRecordedAtCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, _, _ := registriesHarness(t)
	declareRegistries(t, m, tenant, []SessionRegistry{acmeRegistry, {Kind: "npm", URL: "https://registry.npmjs.org"}}, nil)

	dto, err := createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(nil, false))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := m.loadRun(ctx, tenant, dto.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	stored := rec.String(colRunRegistrySecrets)
	if stored != `[{"env":"env/npm-acme","secret":"env/npm-acme"}]` || strings.Contains(stored, registryCanary) {
		t.Fatalf("recorded registry token secrets = %q, want the secret's name only", stored)
	}
	m.rt.dropLive(tenant, dto.RunRef)
	if !m.usedRegistryTokens(ctx, tenant, rec) {
		t.Fatal("a session created with a registry token is not known to have used one")
	}

	// A registry with no token, and a profile with none, record that they used none.
	declareRegistries(t, m, tenant, []SessionRegistry{{Kind: "npm", URL: "https://registry.npmjs.org"}}, nil)
	dto, err = createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(nil, false))
	if err != nil {
		t.Fatal(err)
	}
	if rec, err = m.loadRun(ctx, tenant, dto.RunRef); err != nil || rec.String(colRunRegistrySecrets) != "[]" || m.usedRegistryTokens(ctx, tenant, rec) {
		t.Fatalf("a registry without a token recorded %q, %v", rec.String(colRunRegistrySecrets), err)
	}
}

// What a launch recorded is never narrowed: the tool's transcript keeps what an
// earlier launch printed, whatever the profile declares later.
func TestRegistryTokens_AResumeAddsTokensAndNeverNarrowsThem(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	vault := newFakeVault()
	m, tenant, _, _ := registriesHarness(t, WithProviderSecretVault(vault))
	vault.values[tenant.String()+"|env/npm-acme"] = registryCanary
	vault.values[tenant.String()+"|env/npm-other"] = "npm_OTHERCANARY_registry_token_0123456789"
	ref := declareRegistries(t, m, tenant, []SessionRegistry{acmeRegistry}, nil)

	dto, err := createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(nil, false))
	if err != nil {
		t.Fatal(err)
	}
	stored := func() string {
		rec, err := m.loadRun(ctx, tenant, dto.RunRef)
		if err != nil {
			t.Fatal(err)
		}
		return rec.String(colRunRegistrySecrets)
	}
	resumeWith := func(registries []SessionRegistry) {
		t.Helper()
		if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
			t.Fatal(err)
		}
		programs := []string{}
		if _, err := m.PatchProfile(ctx, tenant, ref, ProfilePatch{SessionRegistries: &registries, SessionPrograms: &programs, MayUseSecretEnv: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", false); err != nil {
			t.Fatalf("resume: %v", err)
		}
	}

	other := SessionRegistry{Kind: "npm", URL: "https://npm2.acme.example/", Scope: "@other", Secret: "env/npm-other"}
	resumeWith([]SessionRegistry{other})
	both := `[{"env":"env/npm-acme","secret":"env/npm-acme"},{"env":"env/npm-other","secret":"env/npm-other"}]`
	if got := stored(); got != both {
		t.Fatalf("after a resume under another registry = %q, want %q", got, both)
	}
	resumeWith([]SessionRegistry{})
	if got := stored(); got != both {
		t.Fatalf("after a resume under none = %q, want %q", got, both)
	}
}

// A session that used no registry token keeps every read it had after a restart,
// and so does one whose profile declares a registry later: it printed none of it.
func TestRegistryTokens_ASessionWithNoRegistryTokenIsStillRead(t *testing.T) {
	f := newMoveFixture(t)
	activityFixtureHome(t, f)
	f.stop(t)
	f.m.rt.dropLive(f.tenant, f.runRef)
	f.declareRegistries(t, []SessionRegistry{acmeRegistry})

	for _, read := range f.transcriptReads() {
		if r := f.h.doJSON("GET", read[1], f.admin, nil, f.hdr); r.code != http.StatusOK {
			t.Fatalf("not live, %s = %d %s", read[0], r.code, r.raw)
		}
	}
	if r := f.post(t, "move", map[string]any{"provider_profile_ref": f.b}); r.code != http.StatusOK {
		t.Fatalf("move = %d %s", r.code, r.raw)
	}
}

// What cannot be proven is refused: a stored value that cannot be read, and a
// session from before the value was recorded whose profile declares a registry
// token now. A legacy session whose profile declares none keeps its reads.
func TestRegistryTokens_WhatCannotBeProvenIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		stored   any
		declared []SessionRegistry
		refused  bool
	}{
		"an unreadable value":                 {stored: "not json", refused: true},
		"no value, a registry token declared": {stored: nil, declared: []SessionRegistry{acmeRegistry}, refused: true},
		"no value, no registry declared":      {stored: nil, refused: false},
	} {
		t.Run(name, func(t *testing.T) {
			f := registryTranscriptFixture(t)
			f.stop(t)
			if tc.declared != nil {
				f.declareRegistries(t, tc.declared)
			}
			setRunColumn(t, f, colRunRegistrySecrets, tc.stored)
			f.m.rt.dropLive(f.tenant, f.runRef)
			if tc.refused {
				f.refusedAsRegistryBearing(t)
				return
			}
			if _, r := f.activity(t); r.code != http.StatusOK {
				t.Fatalf("activity = %d %s", r.code, r.raw)
			}
		})
	}
}

// A queued session that never launched has no transcript to withhold: it keeps
// answering that its transcript is pending.
func TestRegistryTokens_ASessionWithNoConversationYetIsPending(t *testing.T) {
	f := newMoveFixture(t)
	f.stop(t)
	f.declareRegistries(t, []SessionRegistry{acmeRegistry})
	setRunColumn(t, f, colRunRegistrySecrets, `[{"env":"env/npm-acme","secret":"env/npm-acme"}]`)
	setRunColumn(t, f, colClaudeSessionID, nil)
	f.m.rt.dropLive(f.tenant, f.runRef)
	got, r := f.activity(t)
	if r.code != http.StatusOK || got.Transcript != activityTranscriptPending {
		t.Fatalf("activity = %d %s", r.code, r.raw)
	}
}

// registryBearingFixture is a session whose first launch ran under the registry
// token, with a transcript that quotes it.
func registryBearingFixture(t *testing.T) (*moveFixture, *fakeVault) {
	t.Helper()
	vault := newFakeVault()
	f := newMoveFixtureWith(t, func(f *moveFixture) {
		vault.values[f.tenant.String()+"|"+acmeRegistry.Secret] = registryCanary
		f.declareRegistries(t, []SessionRegistry{acmeRegistry})
	}, WithProviderSecretVault(vault), WithClaudeHookPEP(t.TempDir(), "/bin/true"))
	ctx := context.Background()
	if rec, err := f.m.loadRun(ctx, f.tenant, f.runRef); err != nil || !f.m.usedRegistryTokens(ctx, f.tenant, rec) {
		t.Fatalf("the session was not created under a registry token: %v", err)
	}
	registryTranscriptQuoting(t, f)
	return f, vault
}

// liveTranscriptReads checks the reads that pass the live redactor: the activity,
// a subagent's transcript and the messages.
func (f *moveFixture) liveTranscriptReads(t *testing.T, when string, want int) {
	t.Helper()
	reads := append(f.transcriptReads()[:2],
		[2]string{"messages", "/v1/m/sessions/runs/" + f.runRef + "/messages"})
	for _, read := range reads {
		r := f.h.doJSON("GET", read[1], f.admin, nil, f.hdr)
		if r.code != want || strings.Contains(r.raw, registryCanary) {
			t.Fatalf("%s, live %s = %d %s, want %d without the token", when, read[0], r.code, r.raw, want)
		}
		if want == http.StatusConflict && !strings.Contains(r.raw, "package registry token") {
			t.Fatalf("%s, live %s = %s, want the refusal to name the package registry token", when, read[0], r.raw)
		}
	}
}

// A live session's redactor holds the tokens of ITS launch only, and the transcript
// on disk holds what every earlier launch printed. A session whose earlier launch
// opened a token is therefore not read live after a resume, even when the secret was
// rotated in between (the new launch's redactor holds the new value, not the old
// one); one that was never resumed keeps its redacted reads.
func TestRegistryTokens_AResumedLiveSessionIsNotReadThroughTheNewLaunchsRedactor(t *testing.T) {
	f, vault := registryBearingFixture(t)
	f.liveTranscriptReads(t, "never resumed", http.StatusOK)

	// The operator rotates the secret and resumes: the new launch holds only the new
	// value, and the transcript still quotes the old one.
	f.stop(t)
	vault.values[f.tenant.String()+"|"+acmeRegistry.Secret] = "npm_ROTATED_registry_token_0123456789"
	if r := f.post(t, "resume", nil); r.code != http.StatusOK {
		t.Fatalf("resume = %d %s", r.code, r.raw)
	}
	f.liveTranscriptReads(t, "resumed after a rotation", http.StatusConflict)
}

// A first launch that never reported its conversation (it failed before the tool
// started) wrote no transcript a read can reach, so the resume is the launch that
// writes it, and its redactor holds every token that transcript can hold. The
// secret was rotated in between: only the launch's own token is withheld.
func TestRegistryTokens_AResumeBeforeAnyConversationKeepsItsReads(t *testing.T) {
	f, vault := registryBearingFixture(t)
	f.stop(t)
	setRunColumn(t, f, colClaudeSessionID, nil)
	seedClaudeState(t, f.homeA, moveTestSID) // the transcript the new launch writes: no token in it
	vault.values[f.tenant.String()+"|"+acmeRegistry.Secret] = "npm_ROTATED_registry_token_0123456789"
	if r := f.post(t, "resume", nil); r.code != http.StatusOK {
		t.Fatalf("resume = %d %s", r.code, r.raw)
	}
	waitFor(t, "session id capture", func() bool {
		d, _ := f.m.getRun(context.Background(), f.tenant, f.runRef)
		return d.ClaudeSessionID == moveTestSID
	})
	f.liveTranscriptReads(t, "resumed before any conversation", http.StatusOK)
}

// What cannot be proven is refused live as it is when the session is not live: a
// stored value that cannot be read, and a session from before the value was
// recorded whose profile declares a registry token now.
func TestRegistryTokens_AResumedLiveSessionWhatCannotBeProvenIsRefused(t *testing.T) {
	for name, stored := range map[string]any{
		"an unreadable value":                 "not json",
		"no value, a registry token declared": nil,
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := registryBearingFixture(t)
			f.stop(t)
			setRunColumn(t, f, colRunRegistrySecrets, stored)
			if r := f.post(t, "resume", nil); r.code != http.StatusOK {
				t.Fatalf("resume = %d %s", r.code, r.raw)
			}
			f.liveTranscriptReads(t, name, http.StatusConflict)
		})
	}
}

// A launch that waited for an approval is the session's first: no earlier launch
// printed anything, so its redactor holds every token the transcript can hold and
// it keeps its reads. The resume after it is the one that follows a launch.
func TestRegistryTokens_AQueuedFirstLaunchIsNotAResume(t *testing.T) {
	ctx := context.Background()
	gate := &controlledLaunchApproval{}
	runner := &fakeRunner{initSID: "queued-registry"}
	vault := newFakeVault()
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()),
		WithLaunchGate(gate), WithProviderSecretVault(vault), WithClaudeHookPEP(t.TempDir(), "/bin/true"))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "queued-registry")
	vault.values[tenant.String()+"|"+acmeRegistry.Secret] = registryCanary
	if r := h.doJSON("POST", "/v1/users", admin, map[string]any{"email": "queued-registry@fh.invalid", "password": "synthetic-password1", "tenant": tenant.String(), "role": auth.RoleAdmin}, nil); r.code != http.StatusCreated {
		t.Fatalf("tenant admin = %d %s", r.code, r.raw)
	}
	login := h.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": "queued-registry@fh.invalid", "password": "synthetic-password1"}, nil)
	token, _ := login.body["token"].(string)
	issuer := auth.NewAuthenticator(h.st, nil)
	m.QueuedCredentialCapture = issuer.BindQueuedCredential
	m.QueuedLaunchAuthorization = func(ctx context.Context, _ model.TenantID, credential auth.QueuedCredential, _ string, _ model.ID) (auth.Principal, error) {
		return issuer.RevalidateQueuedCredential(ctx, credential)
	}
	ref := declareRegistries(t, m, tenant, []SessionRegistry{acmeRegistry}, nil)

	queued := h.doJSON("POST", "/v1/m/sessions/runs", token, map[string]any{"provider_profile_ref": ref, "name": "queued registry launch"}, tenantHdr(tenant))
	if queued.code != http.StatusAccepted || launchCount(runner) != 0 {
		t.Fatalf("queued = %d %s", queued.code, queued.raw)
	}
	runRef := queued.body["run_ref"].(string)
	if rec, err := m.loadRun(ctx, tenant, runRef); err != nil || !m.usedRegistryTokens(ctx, tenant, rec) {
		t.Fatalf("the queued session does not record its registry token: %v", err)
	}
	gate.approved.Store(true)
	waitFor(t, "the approved launch", func() bool { return launchCount(runner) > 0 })
	waitFor(t, "the live handle", func() bool { _, ok := m.rt.getLive(tenant, runRef); return ok })
	lr, _ := m.rt.getLive(tenant, runRef)
	if !lr.redactsEveryEarlierValue {
		t.Fatal("a launch that waited for approval is the first launch; nothing earlier printed a token")
	}

	if _, err := m.stopRun(ctx, tenant, runRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	if _, err := m.resumeRunAs(ctx, tenant, runRef, actorU, actorKindU, "", false); err != nil {
		t.Fatalf("resume: %v", err)
	}
	lr, ok := m.rt.getLive(tenant, runRef)
	if !ok || lr.redactsEveryEarlierValue {
		t.Fatalf("the resume of a session whose first launch opened a token is live = %v, redacts every token = %v", ok, ok && lr.redactsEveryEarlierValue)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/codex/session"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// codexhookpep_e2e_test.go drives the WHOLE Codex path with nothing faked below the wire:
// a real captured hook payload goes in over HTTP, through the connector's PEP, into the
// governed decider, out to the REAL modules/sessions identity plane and the REAL signed
// ledger, and the observations that come back are the ones modules/sessions folds.
//
// The identity plane here is the real one, not the recordingResolver used elsewhere in this
// package: the mandatory collision (claude:X vs codex:X) is only worth anything if the
// index that guarantees it is actually in play.

// These two payloads are VERBATIM captures from a live `codex exec` run against
// codex-cli 0.145.0 (see an internal design note (not shipped)).
// They are duplicated from the connector's own tests on purpose: this file must exercise
// what Codex actually sends, and a payload edited to suit our parser would prove nothing.
const realPreToolUsePayload = `{"session_id":"019fc4c3-40c5-7371-9c92-7b269d23897b","turn_id":"019fc4c3-4157-7380-867c-474a842a75e5","transcript_path":"/workspace/.s528-probe/sessions/2026/08/03/rollout-2026-08-03T01-15-58-019fc4c3-40c5-7371-9c92-7b269d23897b.jsonl","cwd":"/workspace/.s528-probe","hook_event_name":"PreToolUse","model":"gpt-5.6-terra","permission_mode":"bypassPermissions","tool_name":"Bash","tool_input":{"command":"echo HELLO_S528"},"tool_use_id":"exec-5e34277c-9063-4eb0-95dd-79e6fe3e8960"}`

const realSessionStartPayload = `{"session_id":"019fc4c3-40c5-7371-9c92-7b269d23897b","transcript_path":"/workspace/.s528-probe/sessions/2026/08/03/rollout.jsonl","cwd":"/workspace/.s528-probe","hook_event_name":"SessionStart","model":"gpt-5.6-terra","permission_mode":"bypassPermissions","source":"startup"}`

type codexE2E struct {
	store  store.Store
	tenant model.TenantID
	mod    *sessions.Module
	pep    http.Handler
	seen   []sdkmodel.Observation
}

func newCodexE2E(t *testing.T) *codexE2E {
	t.Helper()
	ctx := context.Background()

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}

	mod := sessions.New()
	st, err := coreengine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", SignEvent: signer.SignEvent,
	}, mod.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "codex-e2e", Slug: "codex-e2e", Status: model.StatusActive})
		if e == nil {
			tenant = org.TenantID
		}
		return e
	}); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	mod.UseData(api.NewModuleData(st))

	e := &codexE2E{store: st, tenant: tenant, mod: mod}

	dec := &codexHookDecider{
		tenant:   tenant,
		authr:    codexAuthenticator{principal: auth.ScopedPrincipal(model.NewID(), "codex-e2e", tenant, auth.RoleEditor)},
		sessions: mod, // the REAL identity plane
		store:    st,
		clock:    func() time.Time { return time.Date(2026, 8, 3, 1, 15, 58, 0, time.UTC) },
		log:      discardLog(),
	}

	// The observer is the connector's emit half: it builds exactly the observations the
	// sessions module folds, and this test keeps them so it can assert their shape.
	observe := func(req session.Request, d session.Decision) {
		if edge, ok := session.EdgeFor(req, d); ok {
			e.seen = append(e.seen, edge)
		}
		if f, ok := session.LifecycleFinding(req, d); ok {
			e.seen = append(e.seen, f)
		}
		if f, ok := session.DenyFinding(req, d); ok {
			e.seen = append(e.seen, f)
		}
	}
	e.pep = session.NewPEP(dec, observe, dec.clock)
	return e
}

func (e *codexE2E) deliver(t *testing.T, payload string) map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload))
	// Every governed call carries a credential. It is not decoration: since the
	// authorization fix, a call with no resolvable principal is denied before anything
	// else happens, and an E2E that omitted it would be exercising the deny path while
	// claiming to exercise the governed one.
	r.Header.Set("Authorization", "Bearer e2e-token")
	w := httptest.NewRecorder()
	e.pep.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("PEP answered %d", w.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decision is not JSON: %v (%s)", err, w.Body.String())
	}
	return m
}

// TestE2E_CodexSessionBecomesOurs is the SG-V cycle for a Codex session, end to end:
// a real SessionStart payload arrives, the session is resolved to a canonical sid, a real
// tool call is governed and anchored, and the close arrives — all against a real store.
func TestE2E_CodexSessionBecomesOurs(t *testing.T) {
	e := newCodexE2E(t)
	const external = "019fc4c3-40c5-7371-9c92-7b269d23897b"

	// 1. CLAIM — the session announces itself.
	e.deliver(t, realSessionStartPayload)

	// 2. EXECUTION — a governed tool call.
	e.deliver(t, realPreToolUsePayload)

	// 3. CLOSE.
	e.deliver(t, `{"session_id":"`+external+`","transcript_path":"/x.jsonl","cwd":"/w","hook_event_name":"SessionEnd","reason":"other"}`)

	// The canonical identity exists and is OURS: an osn_ sid, not Codex's UUID.
	sid, err := e.mod.ResolveSession(context.Background(), e.tenant, sessions.SessionBinding{
		Provider: "codex", ExternalID: external, At: time.Now(),
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.HasPrefix(sid, "osn_") {
		t.Errorf("the canonical sid must be ours (osn_ + UUIDv7), got %q", sid)
	}
	if sid == external {
		t.Error("Codex's own UUID must never be used as our key")
	}

	// The ledger holds the governed decisions, under a codex action.
	if n := ledgerCount(t, e.store, e.tenant, "codex.hook."); n == 0 {
		t.Error("the governed decisions must be anchored")
	}

	// Every emitted fact names the SAME canonical session, and none of them leaks the
	// Codex UUID as the session reference.
	if len(e.seen) == 0 {
		t.Fatal("the cycle must emit observations the sessions module can fold")
	}
	for _, o := range e.seen {
		switch v := o.(type) {
		case sdkmodel.EdgeObservation:
			if v.OriginKind != "session" {
				t.Errorf("an edge must be session-origin or the live view routes it to inventory, got %q", v.OriginKind)
			}
			if v.OriginRef != sid {
				t.Errorf("edge origin = %q, want the canonical sid %q", v.OriginRef, sid)
			}
			if v.Labels[session.LabelEngine] != session.EngineCodex {
				t.Errorf("the edge must declare its engine, got %v", v.Labels)
			}
			if p := v.Labels[session.LabelPosture]; p != session.PostureEnforced && p != session.PostureObserved {
				t.Errorf("the edge must declare an honest posture, got %q", p)
			}
		case sdkmodel.FindingReport:
			if v.SubjectKind != "session" || v.SubjectRef != sid {
				t.Errorf("a finding must be scoped to the canonical session, got %s/%s", v.SubjectKind, v.SubjectRef)
			}
		}
	}
}

// TestE2E_PostureIsNotUniform is the "/sessions must not paint both classes alike" rule at
// the level where it is decidable. A PreToolUse deny PREVENTED the act; a PostToolUse deny
// arrived after the tool already ran. Labeling both "enforced" would sell a control that
// in one case does not exist.
func TestE2E_PostureIsNotUniform(t *testing.T) {
	e := newCodexE2E(t)
	e.deliver(t, realPreToolUsePayload)
	e.deliver(t, `{"session_id":"019fc4c3-40c5-7371-9c92-7b269d23897b","turn_id":"t1","transcript_path":"/x","cwd":"/w","hook_event_name":"PostToolUse","model":"gpt-5.6-terra","permission_mode":"default","tool_name":"Bash","tool_input":{"command":"echo hi"},"tool_use_id":"exec-post-1"}`)

	postures := map[string]int{}
	for _, o := range e.seen {
		if edge, ok := o.(sdkmodel.EdgeObservation); ok {
			postures[edge.Labels[session.LabelPosture]]++
		}
	}
	if postures[session.PostureEnforced] == 0 {
		t.Error("a PreToolUse call is enforceable and must be labeled enforced")
	}
	if postures[session.PostureObserved] == 0 {
		t.Error("a PostToolUse call is NOT enforceable and must be labeled observed — the tool already ran")
	}
}

// TestE2E_ProviderCollision is the mandatory collision, driven through the governed path
// against the real index rather than asserted at the identity plane in isolation:
// claude:X and codex:X with the SAME external id must be TWO sessions.
func TestE2E_ProviderCollision(t *testing.T) {
	e := newCodexE2E(t)
	const shared = "collision-abc"
	ctx := context.Background()

	// The Codex side arrives through the governed PEP, exactly as in production.
	e.deliver(t, `{"session_id":"`+shared+`","transcript_path":"/x","cwd":"/w","hook_event_name":"SessionStart","model":"m","permission_mode":"default","source":"startup"}`)

	// The sid the DECIDER actually minted, taken from what it emitted — not from a
	// second lookup. Without this the test would pass even if the decider bound the
	// session under the wrong provider, because the lookup below would simply mint a
	// fresh codex identity and the two would still differ. Verified by mutation: flipping
	// the decider's binding to "claude" turns this red.
	var mintedByDecider string
	for _, o := range e.seen {
		if f, ok := o.(sdkmodel.FindingReport); ok && f.SubjectKind == "session" {
			mintedByDecider = f.SubjectRef
		}
	}
	if mintedByDecider == "" {
		t.Fatal("the SessionStart must have produced a session-scoped fact carrying the minted sid")
	}

	codexSID, err := e.mod.ResolveSession(ctx, e.tenant, sessions.SessionBinding{Provider: "codex", ExternalID: shared, At: time.Now()})
	if err != nil {
		t.Fatalf("resolve codex: %v", err)
	}
	if codexSID != mintedByDecider {
		t.Fatalf("the decider bound this session under the WRONG provider: it minted %q, but codex:%s resolves to %q", mintedByDecider, shared, codexSID)
	}

	// The Claude side is the same external id under the other provider.
	claudeSID, err := e.mod.ResolveSession(ctx, e.tenant, sessions.SessionBinding{Provider: "claude", ExternalID: shared, At: time.Now()})
	if err != nil {
		t.Fatalf("resolve claude: %v", err)
	}

	if codexSID == claudeSID {
		t.Fatalf("claude:%s and codex:%s collapsed onto ONE identity %q — the provider is part of the key precisely so this cannot happen", shared, shared, codexSID)
	}
	// And both are stable: resolving again returns the same pair, not two more.
	again, _ := e.mod.ResolveSession(ctx, e.tenant, sessions.SessionBinding{Provider: "codex", ExternalID: shared, At: time.Now()})
	if again != codexSID {
		t.Errorf("re-resolving must be stable: %q then %q", codexSID, again)
	}
}

// TestE2E_ProviderCaseDoesNotForkIdentity: SG-00 lowercases the provider in code. If it did
// not, "Codex" and "codex" would be two engines and one session would become two.
func TestE2E_ProviderCaseDoesNotForkIdentity(t *testing.T) {
	e := newCodexE2E(t)
	ctx := context.Background()
	lower, err := e.mod.ResolveSession(ctx, e.tenant, sessions.SessionBinding{Provider: "codex", ExternalID: "case-1", At: time.Now()})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	upper, err := e.mod.ResolveSession(ctx, e.tenant, sessions.SessionBinding{Provider: "Codex", ExternalID: "case-1", At: time.Now()})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if lower != upper {
		t.Errorf("Codex and codex are the same engine; they resolved to %q and %q", lower, upper)
	}
}

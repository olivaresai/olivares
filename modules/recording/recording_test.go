// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package recording_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/recording"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Privileged-session recording tests: deny-closed capture, hash-chained
// frames anchored to the ledger, AC-8 consent, redaction by construction, the
// break-glass mandatory floor, forensic replay and verification.

// A human operator's actions on a recorded surface open a session, append
// chained frames, and anchor the open into the ledger; replay reconstructs the
// timeline with the correlated ledger events.
func TestRecordingCapturesPrivilegedActions(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	if r := h.do("GET", "/v1/m/governance/things?limit=5&q=foo", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/governance/things", op, map[string]any{"name": "x"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("post things = %d %s", r.code, r.raw)
	}

	sess := h.sessions(admin, tenant, "?status=active")
	var opSess map[string]any
	for _, s := range sess {
		if s["subject_kind"] == "user" && s["consent_mode"] == "notice" && s["frames_written"].(float64) >= 2 {
			opSess = s
		}
	}
	if opSess == nil {
		t.Fatalf("no operator session with 2 frames: %v", sess)
	}
	id := opSess["id"].(string)
	if opSess["open_seq"].(float64) <= 0 {
		t.Fatalf("session open not anchored to the ledger: %v", opSess)
	}
	if opSess["gap"].(bool) {
		t.Fatalf("unexpected gap: %v", opSess)
	}
	if opSess["retention_class"] != "privileged-session-recording" {
		t.Fatalf("retention class missing (seam): %v", opSess)
	}

	r := h.do("GET", "/v1/m/recording/sessions/"+id+"/replay", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("replay = %d %s", r.code, r.raw)
	}
	if r.body["schema"] != "olivares.recording/v1" || r.body["semconv"] != "1.41.1" {
		t.Fatalf("replay envelope must pin schema+semconv: %v", r.body)
	}
	frames := r.body["frames"].(map[string]any)["items"].([]any)
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(frames))
	}
	f0 := frames[0].(map[string]any)
	if f0["idx"].(float64) != 1 || f0["namespace"] != "governance" || f0["method"] != "GET" ||
		f0["pattern"] != "/things" || f0["outcome"] != "allowed" {
		t.Fatalf("frame 1 malformed: %v", f0)
	}
	if f0["query_keys"] != "limit,q" {
		t.Fatalf("frame 1 must carry query KEYS only, got %v", f0["query_keys"])
	}
	if _, hasBody := f0["body_sha256"]; hasBody {
		t.Fatalf("a GET carries no body digest: %v", f0)
	}
	f1 := frames[1].(map[string]any)
	if f1["idx"].(float64) != 2 || f1["body_sha256"] == "" || f1["body_bytes"].(float64) <= 0 {
		t.Fatalf("frame 2 must digest the consumed body: %v", f1)
	}
	if f1["prev_hash"] != f0["hash"] {
		t.Fatalf("frame 2 must chain to frame 1: prev=%v tip=%v", f1["prev_hash"], f0["hash"])
	}
	if raw := r.raw; strings.Contains(raw, `"name":"x"`) || strings.Contains(raw, "memberpass1") {
		t.Fatalf("replay leaked request content: %s", raw)
	}
	// The correlated ledger window includes the session-open anchor.
	ledger := r.body["ledger"].([]any)
	foundOpen := false
	for _, le := range ledger {
		if le.(map[string]any)["action"] == "recording.session.open" {
			foundOpen = true
		}
	}
	if !foundOpen {
		t.Fatalf("replay ledger window must include the open anchor: %v", ledger)
	}

	// Verification proves the chain and the anchors.
	r = h.do("GET", "/v1/m/recording/sessions/"+id+"/verify", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["ok"] != true || r.body["tip_match"] != true || r.body["anchors_ok"] != true {
		t.Fatalf("verify = %d %v", r.code, r.body)
	}
}

// The frame trail is immutable at the engine level, and any post-hoc forgery of
// the session tip is detected by verify (tamper-evidence, not trust).
func TestRecordingTamperEvidence(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")
	if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things = %d %s", r.code, r.raw)
	}
	id := h.sessions(admin, tenant, "?status=active")[0]["id"].(string)

	ctx := context.Background()
	// (a) frames are append-only: an UPDATE is refused by the store guards.
	err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("recording.frame"))
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Limit: 1})
		if err != nil {
			return err
		}
		recs[0]["outcome"] = "forged"
		_, err = repo.Update(ctx, recs[0])
		return err
	})
	if !errors.Is(err, store.ErrAppendOnly) {
		t.Fatalf("frame update must hit the append-only guard, got %v", err)
	}
	// (b) a forged session tip breaks verification.
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("recording.session"))
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(id))
		if err != nil {
			return err
		}
		rec["tip_hash"] = strings.Repeat("ab", 32)
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatalf("forge tip: %v", err)
	}
	r := h.do("GET", "/v1/m/recording/sessions/"+id+"/verify", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["ok"] != false {
		t.Fatalf("verify of a forged tip must fail: %d %v", r.code, r.body)
	}
}

// Redaction by construction: credential- and email-shaped URL parameters never
// persist raw; query values are never captured.
func TestRecordingRedaction(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	if r := h.do("GET", "/v1/m/governance/things/alice%40example.com?token=supersecret", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things/{id} = %d %s", r.code, r.raw)
	}
	id := h.sessions(admin, tenant, "?status=active")[0]["id"].(string)
	r := h.do("GET", "/v1/m/recording/sessions/"+id+"/replay", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("replay = %d %s", r.code, r.raw)
	}
	if strings.Contains(r.raw, "alice@example.com") || strings.Contains(r.raw, "supersecret") {
		t.Fatalf("replay leaked an email/credential: %s", r.raw)
	}
	frames := r.body["frames"].(map[string]any)["items"].([]any)
	f0 := frames[0].(map[string]any)
	params := f0["params"].(map[string]any)
	if params["id"] != "[REDACTED]" {
		t.Fatalf("email-shaped param must be redacted, got %v", params)
	}
	if f0["query_keys"] != "token" {
		t.Fatalf("query keys only, got %v", f0["query_keys"])
	}
}

// AC-8 consent (required mode): a new operator is deny-closed (403 with the
// distinct code) until the explicit acknowledgement, which opens their session.
func TestRecordingConsentRequired(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("PUT", "/v1/m/recording/config", admin, map[string]any{
		"namespaces": []string{"governance", "recording"}, "consent": "required",
		"idle_seconds": 1800, "retention_days": 180,
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("put config = %d %s", r.code, r.raw)
	}

	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")
	r = h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant))
	if r.code != http.StatusForbidden || errorCode(r) != "recording_consent_required" {
		t.Fatalf("unacked operator must be 403 recording_consent_required, got %d %s", r.code, r.raw)
	}

	// The notice tells the console what to show; the ack is the consent action.
	r = h.do("GET", "/v1/m/recording/notice", op, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["consent_required"] != true || r.body["acknowledged"] != false {
		t.Fatalf("notice = %d %v", r.code, r.body)
	}
	r = h.do("POST", "/v1/m/recording/ack", op, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["session_id"] == "" {
		t.Fatalf("ack = %d %s", r.code, r.raw)
	}
	if r = h.do("POST", "/v1/m/recording/ack", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("ack must be idempotent, got %d %s", r.code, r.raw)
	}
	if r = h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("post-ack things = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/recording/notice", op, nil, tenantHdr(tenant))
	if r.body["acknowledged"] != true || r.body["consent_required"] != false {
		t.Fatalf("post-ack notice = %v", r.body)
	}
}

// The break-glass floor records EVERY principal — a service token's consume is
// captured even though tokens are otherwise out of recording scope, and no
// tenant config can narrow the floor away.
func TestRecordingBreakGlassFloor(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// Narrow the configurable scope to NOTHING.
	r := h.do("PUT", "/v1/m/recording/config", admin, map[string]any{
		"namespaces": []string{}, "consent": "notice", "idle_seconds": 1800, "retention_days": 180,
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("put config = %d %s", r.code, r.raw)
	}

	svc := h.mintToken(admin, tenant, "editor")
	// Ordinary surface: token + namespace removed => NOT recorded.
	if r := h.do("GET", "/v1/m/governance/things", svc, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things = %d %s", r.code, r.raw)
	}
	// Break-glass permission: ALWAYS recorded, token included.
	if r := h.do("POST", "/v1/m/governance/breakglass/consume", svc, map[string]any{"action": "deploy.apply"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("consume = %d %s", r.code, r.raw)
	}

	sess := h.sessions(admin, tenant, "")
	var tokenSess map[string]any
	for _, s := range sess {
		if s["subject_kind"] == "token" {
			tokenSess = s
		}
	}
	if tokenSess == nil {
		t.Fatalf("no token session despite the break-glass floor: %v", sess)
	}
	if got := tokenSess["frames_written"].(float64); got != 1 {
		t.Fatalf("token session must hold EXACTLY the floor frame (things must not record), got %v", got)
	}
	if tokenSess["consent_mode"] != "auto" {
		t.Fatalf("token consent rides provisioning (auto), got %v", tokenSess["consent_mode"])
	}
}

// Recording is deny-closed: with a recorder wired but unable to persist, a
// recorded surface answers 503 recording_unavailable and the handler never runs.
func TestRecordingUnavailableDenies(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	// A second server whose recorder has NO data handle (mis-wired on purpose).
	broken := recording.New()
	srv2, err := api.New(api.Options{
		Store: h.st, Authenticator: auth.NewAuthenticator(h.st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: mustSigner(t), SetupToken: mustSetupToken(t), Version: "test", Clock: h.clk,
		Modules:  []api.Module{victimModule{}},
		Recorder: broken,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := doAgainst(t, srv2, "GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant))
	if r.code != http.StatusServiceUnavailable || errorCode(r) != "recording_unavailable" {
		t.Fatalf("recorded surface with a broken recorder must be 503 recording_unavailable, got %d %s", r.code, r.raw)
	}
}

// An idle session seals lazily on the next action (new session, idle reason,
// ledger seal anchor), and the sweep seals abandoned ones.
func TestRecordingIdleSealAndSweep(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things = %d %s", r.code, r.raw)
	}
	h.clk.advance(31 * time.Minute) // default idle_seconds = 1800
	if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things after idle = %d %s", r.code, r.raw)
	}

	sealed := h.sessions(admin, tenant, "?status=sealed")
	if len(sealed) != 1 || sealed[0]["seal_reason"] != "idle" || sealed[0]["seal_seq"].(float64) <= 0 {
		t.Fatalf("idle predecessor must be sealed+anchored: %v", sealed)
	}
	active := h.sessions(admin, tenant, "?status=active")
	foundFresh := false
	for _, s := range active {
		if s["subject_kind"] == "user" && s["frames_written"].(float64) == 1 {
			foundFresh = true
		}
	}
	if !foundFresh {
		t.Fatalf("a fresh session must have opened after the idle seal: %v", active)
	}

	// Sweep: abandon the fresh session past the idle window and sweep it sealed.
	h.clk.advance(31 * time.Minute)
	r := h.do("POST", "/v1/m/recording/sweep", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["sealed"].(float64) < 1 {
		t.Fatalf("sweep = %d %v", r.code, r.body)
	}
}

// Long sessions anchor periodically: frame 25 carries a ledger anchor seq and
// verification re-checks it.
func TestRecordingPeriodicAnchor(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	for i := 0; i < 25; i++ {
		if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("things #%d = %d %s", i, r.code, r.raw)
		}
	}
	id := h.sessions(admin, tenant, "?status=active")[0]["id"].(string)
	r := h.do("GET", "/v1/m/recording/sessions/"+id+"/replay?limit=100", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("replay = %d %s", r.code, r.raw)
	}
	frames := r.body["frames"].(map[string]any)["items"].([]any)
	if len(frames) != 25 {
		t.Fatalf("frames = %d", len(frames))
	}
	last := frames[24].(map[string]any)
	if last["idx"].(float64) != 25 || last["anchor_seq"] == nil || last["anchor_seq"].(float64) <= 0 {
		t.Fatalf("frame 25 must carry the periodic ledger anchor: %v", last)
	}
	r = h.do("GET", "/v1/m/recording/sessions/"+id+"/verify", admin, nil, tenantHdr(tenant))
	if r.body["ok"] != true || r.body["anchors_ok"] != true || r.body["anchors_checked"].(float64) < 2 {
		t.Fatalf("verify anchors = %v", r.body)
	}
}

// A delayed/legacy break-glass reviewed finding idempotently seals a bound
// recording. The live governance review path seals in its own transaction.
func TestRecordingSealsOnBreakGlassReview(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	if r := h.do("POST", "/v1/m/governance/breakglass/consume", op, map[string]any{"action": "deploy.apply"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("consume = %d %s", r.code, r.raw)
	}
	sess := h.sessions(admin, tenant, "?status=active")
	var opSess map[string]any
	for _, s := range sess {
		if s["subject_kind"] == "user" && s["subject_user"] != "" && s["cred"] != "" && s["frames_written"].(float64) >= 1 {
			opSess = s
		}
	}
	if opSess == nil {
		t.Fatalf("no operator session: %v", sess)
	}
	id := opSess["id"].(string)

	// Bind the grant (what governance does post-activation) and deliver the
	// post-review finding; the bound session must seal.
	cred, ok := strings.CutPrefix(opSess["cred"].(string), "user:")
	if !ok || cred == "" {
		t.Fatalf("operator session has invalid credential witness: %v", opSess)
	}
	prin := auth.Principal{
		Kind:   auth.KindUser,
		UserID: model.ID(opSess["subject_user"].(string)),
		CredID: model.ID(cred),
	}
	if err := h.rec.BindGrant(context.Background(), tenant, model.ID(id), model.ID("0198abcd-0000-7000-8000-000000000001"), prin); err != nil {
		t.Fatalf("bind grant: %v", err)
	}
	h.host.deliver(t, tenant, sdkmodel.FindingReport{
		Kind: "governance_breakglass_reviewed", SubjectKind: "breakglass",
		SubjectRef: "0198abcd-0000-7000-8000-000000000001", Severity: sdkmodel.SeverityInfo,
	})

	sealed := h.sessions(admin, tenant, "?status=sealed")
	found := false
	for _, s := range sealed {
		if s["id"] == id && s["seal_reason"] == "breakglass_review" && s["breakglass_grant"] == "0198abcd-0000-7000-8000-000000000001" {
			found = true
		}
	}
	if !found {
		t.Fatalf("bound session must seal on the reviewed finding: %v", sealed)
	}
}

// Viewing recordings is admin-tier (operator privacy): a viewer reads the
// notice but never the sessions; every session read self-audits to the ledger.
func TestRecordingReadsAreGatedAndAudited(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, viewer := h.roleUser(admin, tenant, "v@x.io", "viewer")

	if r := h.do("GET", "/v1/m/recording/notice", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("viewer notice = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/recording/sessions", viewer, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer sessions must be 403, got %d %s", r.code, r.raw)
	}

	// The admin's list lands on the ledger (privileged-read self-audit).
	if r := h.do("GET", "/v1/m/recording/sessions", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("admin sessions = %d %s", r.code, r.raw)
	}
	foundAudit := false
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(ev model.AuditEvent) error {
			if ev.Action == "recording.read" {
				foundAudit = true
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if !foundAudit {
		t.Fatal("listing recordings must self-audit (recording.read)")
	}
}

// The summary is a DERIVED artifact through the optional port: 501 unwired,
// stored marked-derived when wired, and the transcript carries no raw content.
func TestRecordingSummarize(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")
	if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things = %d %s", r.code, r.raw)
	}
	id := h.sessions(admin, tenant, "?status=active")[0]["id"].(string)

	// Unwired => honest 501.
	if r := h.do("POST", "/v1/m/recording/sessions/"+id+"/summarize", admin, nil, tenantHdr(tenant)); r.code != http.StatusNotImplemented {
		t.Fatalf("summarize without a port must be 501, got %d %s", r.code, r.raw)
	}
}

// Tokens are recorded on configured surfaces too: an operator who mints a
// token to drive the consoles cannot step out of the recording (the insider
// one-step bypass the review caught).
func TestRecordingTokenOnConfiguredSurface(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	svc := h.mintToken(admin, tenant, "editor")

	if r := h.do("GET", "/v1/m/governance/things", svc, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things = %d %s", r.code, r.raw)
	}
	sess := h.sessions(admin, tenant, "?status=active")
	found := false
	for _, s := range sess {
		if s["subject_kind"] == "token" && s["frames_written"].(float64) >= 1 && s["consent_mode"] == "auto" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a token's action on a configured surface must be recorded: %v", sess)
	}
}

// Flipping consent to "required" bites ACTIVE sessions too: the next gate
// seals the notice-mode session and forces the explicit acknowledgement.
func TestRecordingConsentFlipSealsActiveSessions(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things = %d %s", r.code, r.raw)
	}
	r := h.do("PUT", "/v1/m/recording/config", admin, map[string]any{
		"namespaces": []string{"governance", "recording"}, "consent": "required",
		"idle_seconds": 1800, "retention_days": 180,
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("put config = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant))
	if r.code != http.StatusForbidden || errorCode(r) != "recording_consent_required" {
		t.Fatalf("active notice-mode operator must be re-gated after the flip, got %d %s", r.code, r.raw)
	}
	// The flip gates the ADMIN too (they are an operator on a recorded surface);
	// acknowledge before using the console.
	if r := h.do("POST", "/v1/m/recording/ack", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("admin ack = %d %s", r.code, r.raw)
	}
	sealed := h.sessions(admin, tenant, "?status=sealed")
	foundConsentSeal := false
	for _, s := range sealed {
		if s["seal_reason"] == "consent_change" {
			foundConsentSeal = true
		}
	}
	if !foundConsentSeal {
		t.Fatalf("the notice-mode session must seal with reason consent_change: %v", sealed)
	}
	if r = h.do("POST", "/v1/m/recording/ack", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("ack = %d %s", r.code, r.raw)
	}
	if r = h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("post-ack things = %d %s", r.code, r.raw)
	}
}

// A frame whose reserved session was sealed mid-request is DROPPED with a loud
// error (gap evidence) — never appended to a newer session's chain.
func TestRecordSealedMidRequestDropsLoudly(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	uid, op := h.roleUser(admin, tenant, "op@x.io", "admin")
	if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things = %d %s", r.code, r.raw)
	}
	var opSess map[string]any
	for _, s := range h.sessions(admin, tenant, "?status=active") {
		if s["subject_user"] == uid {
			opSess = s
		}
	}
	if opSess == nil {
		t.Fatal("no operator session")
	}
	id := opSess["id"].(string)
	if r := h.do("POST", "/v1/m/recording/sessions/"+id+"/seal", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("seal = %d %s", r.code, r.raw)
	}
	// Simulate the in-flight request whose Gate reserved on the now-sealed
	// session: Record must refuse rather than re-resolve to another session.
	call := api.RecordedCall{Namespace: "governance", Method: "GET", Pattern: "/things", Tenant: model.TenantID(tenant.String())}
	dec := api.RecordingDecision{Record: true, Session: model.ID(id)}
	err := h.rec.Record(context.Background(), call, dec, api.RecordedResult{Status: 200})
	if err == nil || !strings.Contains(err.Error(), "sealed mid-request") {
		t.Fatalf("Record into a sealed session must fail loudly, got %v", err)
	}
}

// A panicking handler still leaves its frame: the deferred Record appends it
// with status 500 while the engine recoverer answers the client.
func TestRecordingSurvivesHandlerPanic(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	uid, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	if r := h.do("GET", "/v1/m/governance/explode", op, nil, tenantHdr(tenant)); r.code != http.StatusInternalServerError {
		t.Fatalf("explode = %d %s", r.code, r.raw)
	}
	var opSess map[string]any
	for _, s := range h.sessions(admin, tenant, "?status=active") {
		if s["subject_user"] == uid {
			opSess = s
		}
	}
	if opSess == nil {
		t.Fatal("panicking handler left no frame")
	}
	if opSess["gap"].(bool) {
		t.Fatalf("the panic frame must be written, not gapped: %v", opSess)
	}
	id := opSess["id"].(string)
	r := h.do("GET", "/v1/m/recording/sessions/"+id+"/replay", admin, nil, tenantHdr(tenant))
	f0 := r.body["frames"].(map[string]any)["items"].([]any)[0].(map[string]any)
	if f0["http_status"].(float64) != 500 || f0["outcome"] != "error" {
		t.Fatalf("panic frame must record 500/error: %v", f0)
	}
}

// A bare query segment (no '=') lands its whole token in key position — the
// key redactor must catch a secret- or email-shaped key.
func TestRecordingQueryKeyRedaction(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	uid, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	if r := h.do("GET", "/v1/m/governance/things?olvs_sel_secretvalue", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things = %d %s", r.code, r.raw)
	}
	var opSess map[string]any
	for _, s := range h.sessions(admin, tenant, "?status=active") {
		if s["subject_user"] == uid {
			opSess = s
		}
	}
	id := opSess["id"].(string)
	r := h.do("GET", "/v1/m/recording/sessions/"+id+"/replay", admin, nil, tenantHdr(tenant))
	if strings.Contains(r.raw, "olvs_sel_secretvalue") {
		t.Fatalf("a secret-shaped query KEY persisted raw: %s", r.raw)
	}
	f0 := r.body["frames"].(map[string]any)["items"].([]any)[0].(map[string]any)
	if f0["query_keys"] != "[REDACTED]" {
		t.Fatalf("query key must be redacted, got %v", f0["query_keys"])
	}
}

// The recorded-namespace config is validated against the namespaces actually
// mounted: a typo cannot silently un-record a surface.
func TestRecordingConfigRejectsUnknownNamespace(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	r := h.do("PUT", "/v1/m/recording/config", admin, map[string]any{
		"namespaces": []string{"goverance"}, "consent": "notice",
		"idle_seconds": 1800, "retention_days": 180,
	}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("typo'd namespace must be rejected, got %d %s", r.code, r.raw)
	}
	r = h.do("PUT", "/v1/m/recording/config", admin, map[string]any{
		"namespaces": []string{"Governance"}, "consent": "notice",
		"idle_seconds": 1800, "retention_days": 180,
	}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("case-mismatched namespace must be rejected, got %d %s", r.code, r.raw)
	}
}

// TestExport_SummaryFormat verifies that format=summary returns a plain-text
// timeline with the expected headers and session ID.
func TestExport_SummaryFormat(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things = %d %s", r.code, r.raw)
	}

	sess := h.sessions(admin, tenant, "?status=active")
	if len(sess) == 0 {
		t.Fatal("expected at least one active session")
	}
	id := sess[0]["id"].(string)

	r := h.do("GET", "/v1/m/recording/sessions/"+id+"/export?format=summary", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("export summary: expected 200, got %d: %s", r.code, r.raw)
	}
	ct := r.header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("expected text/plain Content-Type, got %q", ct)
	}
	if !strings.Contains(r.raw, "--- Timeline ---") {
		t.Errorf("expected Timeline header in summary body, got:\n%s", r.raw)
	}
	if !strings.Contains(r.raw, "Session: "+id) {
		t.Errorf("expected session ID in summary body, got:\n%s", r.raw)
	}
}

// TestExport_JSONFormat verifies that format=json (and the default) returns a
// JSON document with session and frames fields.
func TestExport_JSONFormat(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things = %d %s", r.code, r.raw)
	}

	sess := h.sessions(admin, tenant, "?status=active")
	if len(sess) == 0 {
		t.Fatal("expected at least one active session")
	}
	id := sess[0]["id"].(string)

	// Explicit format=json.
	r := h.do("GET", "/v1/m/recording/sessions/"+id+"/export?format=json", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("export json: expected 200, got %d: %s", r.code, r.raw)
	}
	if r.body["session"] == nil {
		t.Error("export JSON: missing session field")
	}
	if r.body["frames"] == nil {
		t.Error("export JSON: missing frames field")
	}
	// The returned session must identify this session.
	sessBody, _ := r.body["session"].(map[string]any)
	if sessBody["id"] != id {
		t.Errorf("export JSON session.id = %v, want %s", sessBody["id"], id)
	}

	// Default (no format param) must also return JSON.
	r2 := h.do("GET", "/v1/m/recording/sessions/"+id+"/export", admin, nil, tenantHdr(tenant))
	if r2.code != http.StatusOK {
		t.Fatalf("export default: expected 200, got %d: %s", r2.code, r2.raw)
	}
	if r2.body["session"] == nil || r2.body["frames"] == nil {
		t.Error("export default: missing session or frames field")
	}
}

// TestExport_BadFormat verifies that an unsupported format parameter returns 400.
func TestExport_BadFormat(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things = %d %s", r.code, r.raw)
	}

	sess := h.sessions(admin, tenant, "?status=active")
	if len(sess) == 0 {
		t.Fatal("expected at least one active session")
	}
	id := sess[0]["id"].(string)

	r := h.do("GET", "/v1/m/recording/sessions/"+id+"/export?format=xml", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("export bad format: expected 400, got %d: %s", r.code, r.raw)
	}
}

// fakeSummarizer is a deterministic Summarizer for the opt-in/coverage tests.
type fakeSummarizer struct{ lastTranscript string }

func (f *fakeSummarizer) Summarize(_ context.Context, _ model.TenantID, transcript string) (string, error) {
	f.lastTranscript = transcript
	return "summary: routine governance reads, nothing anomalous", nil
}

// AI summaries are opt-in per tenant (the transcript leaves the trust
// boundary), sealed-only (a partial summary would masquerade as the
// session's), and coverage-bound in summary_meta.
func TestRecordingSummarizeGovernedFlow(t *testing.T) {
	fake := &fakeSummarizer{}
	h := newHarness(t, recording.WithSummarizer(fake))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	uid, op := h.roleUser(admin, tenant, "op@x.io", "admin")
	if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things = %d %s", r.code, r.raw)
	}
	var opSess map[string]any
	for _, s := range h.sessions(admin, tenant, "?status=active") {
		if s["subject_user"] == uid {
			opSess = s
		}
	}
	id := opSess["id"].(string)

	// Opt-in off => 403 (deny-closed third-party egress).
	if r := h.do("POST", "/v1/m/recording/sessions/"+id+"/summarize", admin, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("summarize without the tenant opt-in must be 403, got %d %s", r.code, r.raw)
	}
	r := h.do("PUT", "/v1/m/recording/config", admin, map[string]any{
		"namespaces": []string{"governance", "recording"}, "consent": "notice",
		"idle_seconds": 1800, "retention_days": 180, "ai_summaries": true,
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("put config = %d %s", r.code, r.raw)
	}
	// Active session => 409 (sealed-only).
	if r := h.do("POST", "/v1/m/recording/sessions/"+id+"/summarize", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("summarize of an active session must be 409, got %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/recording/sessions/"+id+"/seal", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("seal = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/m/recording/sessions/"+id+"/summarize", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["summary"] == "" {
		t.Fatalf("summarize = %d %s", r.code, r.raw)
	}
	meta := r.body["summary_meta"].(map[string]any)
	if meta["derived"] != true || meta["frames"].(float64) < 1 || meta["tip_hash"] == "" {
		t.Fatalf("summary_meta must bind coverage (derived, frames, tip_hash): %v", meta)
	}
	if strings.Contains(fake.lastTranscript, "olvs_") || strings.Contains(fake.lastTranscript, "@x.io") {
		t.Fatalf("transcript leaked a credential/email: %s", fake.lastTranscript)
	}
}

// The extended session list filters — seal_reason, opened_after/before, and
// subject_contains — each narrow the result set without changing the shape of
// the items they return.
func TestListSessions_FilterBySealReason(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	// Open an active session.
	if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things = %d %s", r.code, r.raw)
	}
	// Advance clock past the idle threshold (default 1800s).
	h.clk.advance(31 * time.Minute)
	// The next action lazy-seals the idle session with reason "idle".
	if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things after idle = %d %s", r.code, r.raw)
	}

	// seal_reason=idle must return only the idle-sealed sessions.
	idleSess := h.sessions(admin, tenant, "?seal_reason=idle")
	if len(idleSess) == 0 {
		t.Fatal("seal_reason=idle must return the idle-sealed session")
	}
	for _, s := range idleSess {
		if s["seal_reason"] != "idle" {
			t.Fatalf("seal_reason=idle filter returned a session with seal_reason=%q: %v", s["seal_reason"], s)
		}
	}

	// seal_reason=closed must not return idle-sealed sessions.
	closedSess := h.sessions(admin, tenant, "?seal_reason=closed")
	for _, s := range closedSess {
		if s["seal_reason"] == "idle" {
			t.Fatalf("seal_reason=closed filter must not return idle-sealed sessions: %v", s)
		}
	}
}

func TestListSessions_FilterByOpenedAfterBefore(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op1 := h.roleUser(admin, tenant, "op1@x.io", "admin")
	_, op2 := h.roleUser(admin, tenant, "op2@x.io", "admin")

	// op1 creates a session at baseTime.
	if r := h.do("GET", "/v1/m/governance/things", op1, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("op1 things = %d %s", r.code, r.raw)
	}
	// A midpoint 5 minutes after baseTime (before op2's session).
	midpoint := model.NewTimestamp(baseTime.Add(5 * time.Minute)).String()

	// Advance clock by 10 minutes so op2's session opens at a strictly later time.
	h.clk.advance(10 * time.Minute)
	if r := h.do("GET", "/v1/m/governance/things", op2, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("op2 things = %d %s", r.code, r.raw)
	}

	// opened_after=midpoint: all returned sessions must have opened_at >= midpoint.
	after := h.sessions(admin, tenant, "?opened_after="+midpoint)
	if len(after) == 0 {
		t.Fatal("opened_after filter must return at least op2's session")
	}
	for _, s := range after {
		if s["opened_at"].(string) < midpoint {
			t.Fatalf("opened_after=%s returned session with opened_at=%s: %v", midpoint, s["opened_at"], s)
		}
	}

	// opened_before=midpoint: all returned sessions must have opened_at <= midpoint.
	before := h.sessions(admin, tenant, "?opened_before="+midpoint)
	if len(before) == 0 {
		t.Fatal("opened_before filter must return at least op1's session")
	}
	for _, s := range before {
		if s["opened_at"].(string) > midpoint {
			t.Fatalf("opened_before=%s returned session with opened_at=%s: %v", midpoint, s["opened_at"], s)
		}
	}
}

func TestListSessions_FilterBySubjectContains(t *testing.T) {
	const contract = `RECORDINGS_LIKE_LITERAL_CONTRACT: subject_contains must escape %, _ and \\ before adding contains wildcards`

	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	uid, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	// Create a user session.
	if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("things = %d %s", r.code, r.raw)
	}
	// Also create a token session on the break-glass floor.
	svc := h.mintToken(admin, tenant, "editor")
	if r := h.do("POST", "/v1/m/governance/breakglass/consume", svc, map[string]any{"action": "deploy.apply"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("breakglass consume = %d %s", r.code, r.raw)
	}

	// subject_contains=user: must return only user sessions (not token sessions).
	userSess := h.sessions(admin, tenant, "?subject_contains=user:")
	if len(userSess) == 0 {
		t.Fatalf("%s: ordinary text returned no populated result", contract)
	}
	for _, s := range userSess {
		sub, _ := s["subject"].(string)
		if !strings.HasPrefix(sub, "user:") {
			t.Fatalf("%s: ordinary text returned subject=%q: %v", contract, sub, s)
		}
	}

	// subject_contains=<uid> must return only this user's sessions.
	uidSess := h.sessions(admin, tenant, "?subject_contains="+uid)
	if len(uidSess) == 0 {
		t.Fatalf("%s: subject_contains=%s did not return the operator's session", contract, uid)
	}
	for _, s := range uidSess {
		sub, _ := s["subject"].(string)
		if !strings.Contains(sub, uid) {
			t.Fatalf("%s: subject_contains=%s returned subject=%q: %v", contract, uid, sub, s)
		}
	}

	// SQL LIKE metacharacters are ordinary search text. None occurs in the
	// fixture subjects, so each query must return the empty response rather than
	// widening to every session (as bare % and _ would).
	for _, literal := range []string{"%", "_", `\`} {
		sessions := h.sessions(admin, tenant, "?subject_contains="+url.QueryEscape(literal))
		if len(sessions) != 0 {
			t.Fatalf("%s: literal %q widened to %d sessions: %v", contract, literal, len(sessions), sessions)
		}
	}
}

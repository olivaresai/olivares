// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// These tests pin ONE claim, path by path: the residual scan that runs after the
// erase pass and before the crypto-shred STATES WHAT IT OPENED, against what the
// request's own data-class scope required, and the sealed receipt carries that
// statement. They are the receipt half of a finding that a certificate stated a
// REQUESTED verification tier as if it had run.
//
// The rules under test, in the words the tests assert them in:
//   - ONE SPEAKER. Only the pre-shred scan writes residual_scan_depth,
//     residual_scan_opened and residual_scan_applicable. A post-shred coordinator
//     may declare its OWN failure, on its own marker, and nothing else about depth.
//   - DEPTH IFF ESTABLISHED. A sealed receipt carries a depth exactly when the scan
//     opened at least one target.
//   - AT MOST ONE SCAN DECLARATION, AND EXACTLY WHEN. "opened nothing" and "opened
//     less than the scope required" are mutually exclusive conditions, so the two
//     sentences can never co-occur.
//   - ANY DECLARATION FLIPS THE VERDICT. verify_ok is true only if the scan declared
//     nothing. An erasure that opened nothing is not a verified erasure.
//   - NO RECEIPT, NO CLAIM. A scan that failed seals nothing at all.

// The three machine-matchable substrings a consumer matches on. They are disjoint by
// construction and are written here VERBATIM, independently of any constant the
// implementation may share, so a reworded declaration is a failing test and not a
// silent contract change.
const (
	wantDepthMarker    = "residual-scan-depth=unestablished"
	wantCoverageMarker = "residual-scan-coverage=partial"
	wantReScanMarker   = "residual-rescan=unverified"

	wantUnestablishedSentence = "residual-scan-depth=unestablished: the residual scan opened no target " +
		"for this subject kind and data-class scope, so no surviving identifier could have been observed"

	// The partial sentence for the one deployment shape this tree can build: a
	// `session` subject whose scope requires four targets in a control plane where
	// two of the owning modules are not registered.
	wantPartialSentence = "residual-scan-coverage=partial: the residual scan opened 2 of the 4 targets " +
		"this request's data-class scope required; not opened: knowledge.memory_scoped, voice.session"

	// The depth value: it names the METHOD, never a coverage claim.
	wantDepth = "registry-scoped"
)

// The four targets a full-coverage `agent` erasure opens, in registry order
// (erasuretargets.go:88-155 restricted to the kinds carrying an agent_ref).
var wantAgentTargets = []string{"knowledge.memory", "knowledge.memory_scoped", "sessions.live", "voice.session"}

// The four targets a `session` erasure's scope REQUIRES, in registry order.
var wantSessionApplicable = []string{"knowledge.memory_scoped", "sessions.live", "sessions.timeline", "voice.session"}

// ---- harness ---------------------------------------------------------------------

// newScanHarness is the module harness with the stand-in registration under the
// test's control. The shared harness registers EVERY erasure target, which is the
// right default and is exactly why it cannot reach the partial-coverage path: there,
// the store must be missing a kind the request's scope names.
func newScanHarness(t *testing.T, standIns func(store.ExtensionRegistry) error, opts ...Option) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t}

	opts = append([]Option{WithClock(fixedClock{t: time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)})}, opts...)
	mod := New(opts...)
	h.mod = mod

	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true},
		func(reg store.ExtensionRegistry) error {
			if err := mod.RegisterSchema(reg); err != nil {
				return err
			}
			return standIns(reg)
		})
	if err != nil {
		t.Fatal(err)
	}
	h.st = st
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	mod.UseData(api.NewModuleData(st))

	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", Modules: []api.Module{mod},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.srv, h.setupTok = srv, plaintext
	return h
}

// newPartialCoverageHarness registers the two session stores whose owning modules are
// present, and NOT knowledge.memory_scoped or voice.session — the deployment a
// partial-coverage declaration is about: the request's scope names four stores and
// this control plane can open two of them. The scan must say so; it must not report
// a clean sweep of the two it could reach.
func newPartialCoverageHarness(t *testing.T, opts ...Option) *harness {
	t.Helper()
	return newScanHarness(t, func(reg store.ExtensionRegistry) error {
		if err := reg.Register(model.EntityDescriptor{
			Kind:  sessionsLiveStandInKind,
			Table: "sessions_live",
			Fields: []model.FieldSpec{
				{Name: "session_ref", Kind: model.KindText},
				{Name: "agent_ref", Kind: model.KindText, Nullable: true},
				{Name: "event_count", Kind: model.KindInt},
			},
		}); err != nil {
			return err
		}
		return reg.Register(model.EntityDescriptor{
			Kind:  sessionsTimelineStandInKind,
			Table: "sessions_timeline",
			Fields: []model.FieldSpec{
				{Name: "session_ref", Kind: model.KindText, Indexed: true},
				{Name: "at", Kind: model.KindTimestamp},
				{Name: "kind", Kind: model.KindText},
			},
		})
	}, opts...)
}

// errScanUnavailable is the store failure the two "seals nothing" paths inject.
var errScanUnavailable = errors.New("residual scan read view unavailable (test)")

// scanFailingData wraps the module's data handle so a test can fail the ONE read view
// the residual scan opens, without touching the request-scoped handle the handler
// writes the request's status through. armed is flipped from the provider leg, which
// runs after the last hold re-check and immediately before the scan.
type scanFailingData struct {
	inner     api.ModuleData
	armed     bool
	afterScan bool // true: let the scan's own loop run, then fail the view it ran in
	scanRan   bool
}

func (d *scanFailingData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if !d.armed {
		return d.inner.View(ctx, tenant, fn)
	}
	if !d.afterScan {
		return errScanUnavailable
	}
	return d.inner.View(ctx, tenant, func(sc store.Scope) error {
		if err := fn(sc); err != nil {
			return err
		}
		d.scanRan = true
		return errScanUnavailable
	})
}

func (d *scanFailingData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.inner.Mutate(ctx, tenant, fn)
}

// ---- small readers over the receipt JSON -------------------------------------------

func jsonText(v any) string { s, _ := v.(string); return s }

func jsonStrings(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, jsonText(it))
	}
	return out
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// approvedGate is the two-human approval every execute below needs.
func approvedGate(ref string) *stubApprovalGate {
	g := &stubApprovalGate{}
	g.set(GateStatusApproved, ref, "alice", "bob")
	return g
}

// wiredProvider is a provider leg that RAN, so the only thing that can put gaps on
// these receipts is the scan itself.
func wiredProvider() *stubProviderEraser {
	return &stubProviderEraser{outcome: ProviderEraseOutcome{Wired: true, Enumerated: 1, Erased: 1}}
}

// wiredAccount is the same for the account leg, which only a `user` subject reaches.
func wiredAccount() *stubAccountEraser {
	return &stubAccountEraser{outcome: AccountEraseOutcome{Attempted: true, Erased: 1, Detail: "1 account anonymized"}}
}

// runErasure creates and executes one erasure and returns the execute response (the
// sealed receipt on 200) and the request's terminal status.
func runErasure(t *testing.T, h *harness, owner string, tenant model.TenantID, kind, ref, caseRef string, classes []string) (resp, string) {
	t.Helper()
	hdr := tenantHdr(tenant)
	body := map[string]any{"subject_kind": kind, "subject_ref": ref, "case_ref": caseRef}
	if len(classes) > 0 {
		body["data_classes"] = classes
	}
	created := h.do("POST", "/v1/m/compliance/erasure", owner, body, hdr)
	if created.code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.code, created.raw)
	}
	id := jsonText(created.body["id"])
	out := h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr)
	status := jsonText(h.do("GET", "/v1/m/compliance/erasure/"+id, owner, nil, hdr).body["status"])
	return out, status
}

// openTenant is the setup every test below repeats: an admin, an org and an owner.
func openTenant(t *testing.T, h *harness, slug string) (model.TenantID, string) {
	t.Helper()
	admin := h.adminLogin()
	tenant := h.createOrg(admin, slug)
	return tenant, h.roleToken(admin, tenant, "owner@x.io", "owner")
}

// ---- row 1: full coverage ----------------------------------------------------------

// TestFullCoverageStatesTheDepth is row 1u: the scan opened every target the scope
// required, so the receipt states the METHOD it used and both label sets, and it
// declares nothing. A receipt that says nothing about what it looked at is the defect
// this whole change is about — a clean verdict must be a clean verdict OF SOMETHING.
func TestFullCoverageStatesTheDepth(t *testing.T) {
	h := newHarness(t, WithApprovalGate(approvedGate("apr-1u")), WithProviderEraser(wiredProvider()))
	tenant, owner := openTenant(t, h, "scanfull")
	seedSubjectRows(h, tenant, "agent-full")

	r, status := runErasure(t, h, owner, tenant, "agent", "agent-full", "DSR-1U", nil)
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if got := jsonText(r.body["residual_scan_depth"]); got != wantDepth {
		t.Fatalf("residual_scan_depth = %q, want %q — a scan that opened four targets must say how it looked: %s", got, wantDepth, r.raw)
	}
	if got := jsonStrings(r.body["residual_scan_opened"]); !sameStrings(got, wantAgentTargets) {
		t.Fatalf("residual_scan_opened = %v, want %v", got, wantAgentTargets)
	}
	if got := jsonStrings(r.body["residual_scan_applicable"]); !sameStrings(got, wantAgentTargets) {
		t.Fatalf("residual_scan_applicable = %v, want %v", got, wantAgentTargets)
	}
	if got := jsonStrings(r.body["data_classes"]); !sameStrings(got, affectedClasses("agent")) {
		t.Fatalf("data_classes = %v, want %v (the scope the two sets are read against)", got, affectedClasses("agent"))
	}
	why := jsonText(r.body["verify_reason"])
	if strings.Contains(why, wantDepthMarker) || strings.Contains(why, wantCoverageMarker) {
		t.Fatalf("full coverage declared something: verify_reason = %q", why)
	}
	if r.body["verify_ok"] != true {
		t.Fatalf("verify_ok = %v, want true: %s", r.body["verify_ok"], r.raw)
	}
	if status != erasureStatusCompleted {
		t.Fatalf("status = %q, want %q", status, erasureStatusCompleted)
	}
}

// TestWiredFullCoverageAddsNoScanText is row 1w: the same input with a coordinator
// wired seals the SAME depth and the SAME two label sets, and the coordinator adds no
// text about the scan. Build parity is the property that makes the field readable at
// all — a regulator cannot ask which binary sealed a receipt.
func TestWiredFullCoverageAddsNoScanText(t *testing.T) {
	coord := &stubCryptoShredCoordinator{}
	h := newHarness(t, WithApprovalGate(approvedGate("apr-1w")), WithProviderEraser(wiredProvider()),
		WithCryptoShredCoordinator(coord))
	tenant, owner := openTenant(t, h, "scanfullw")
	seedSubjectRows(h, tenant, "agent-full")

	r, status := runErasure(t, h, owner, tenant, "agent", "agent-full", "DSR-1W", nil)
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if coord.verifyCalls != 1 {
		t.Fatalf("coordinator verify calls = %d, want 1 (the wired build must really run)", coord.verifyCalls)
	}
	if got := jsonText(r.body["residual_scan_depth"]); got != wantDepth {
		t.Fatalf("wired residual_scan_depth = %q, want %q (byte-identical to the un-wired build): %s", got, wantDepth, r.raw)
	}
	if got := jsonStrings(r.body["residual_scan_opened"]); !sameStrings(got, wantAgentTargets) {
		t.Fatalf("wired residual_scan_opened = %v, want %v", got, wantAgentTargets)
	}
	why := jsonText(r.body["verify_reason"])
	if strings.Contains(why, "scan_depth=") {
		t.Fatalf("the coordinator's summary put a second depth beside the field: %q", why)
	}
	if strings.Contains(why, wantDepthMarker) || strings.Contains(why, wantCoverageMarker) {
		t.Fatalf("wired full coverage declared something: verify_reason = %q", why)
	}
	if r.body["verify_ok"] != true || status != erasureStatusCompleted {
		t.Fatalf("wired verdict = %v / %q, want true / %q: %s", r.body["verify_ok"], status, erasureStatusCompleted, r.raw)
	}
}

// ---- row 2: partial coverage --------------------------------------------------------

// TestPartialCoverageIsStatedBesideTheDepth is row 2u: the scan opened two of the four
// targets the request's own scope required. The depth is still stated — the method did
// run — and the shortfall is declared beside it, naming the stores it could not open.
// The verdict flips: the scan could not look where the request said to look.
func TestPartialCoverageIsStatedBesideTheDepth(t *testing.T) {
	h := newPartialCoverageHarness(t, WithApprovalGate(approvedGate("apr-2u")), WithProviderEraser(wiredProvider()))
	tenant, owner := openTenant(t, h, "scanpartial")

	r, status := runErasure(t, h, owner, tenant, "session", "sess-partial", "DSR-2U", nil)
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if got := jsonText(r.body["residual_scan_depth"]); got != wantDepth {
		t.Fatalf("residual_scan_depth = %q, want %q (a partial scan still used the method): %s", got, wantDepth, r.raw)
	}
	wantOpened := []string{"sessions.live", "sessions.timeline"}
	if got := jsonStrings(r.body["residual_scan_opened"]); !sameStrings(got, wantOpened) {
		t.Fatalf("residual_scan_opened = %v, want %v", got, wantOpened)
	}
	if got := jsonStrings(r.body["residual_scan_applicable"]); !sameStrings(got, wantSessionApplicable) {
		t.Fatalf("residual_scan_applicable = %v, want %v", got, wantSessionApplicable)
	}
	why := jsonText(r.body["verify_reason"])
	if !strings.Contains(why, wantPartialSentence) {
		t.Fatalf("verify_reason does not carry the coverage declaration verbatim.\n got: %q\nwant substring: %q", why, wantPartialSentence)
	}
	if strings.Contains(why, wantDepthMarker) {
		t.Fatalf("both scan declarations on one receipt: %q", why)
	}
	if r.body["verify_ok"] != false {
		t.Fatalf("verify_ok = %v, want false — a declared shortfall is not a verified erasure: %s", r.body["verify_ok"], r.raw)
	}
	if status != erasureStatusGaps {
		t.Fatalf("status = %q, want %q", status, erasureStatusGaps)
	}
}

// TestWiredPartialCoverageSaysItOnce is row 2w: the wired build seals the SAME one
// sentence, ONCE. Two speakers on one field is the defect the shape removes; a second
// copy of the sentence would be the same defect wearing the same words.
func TestWiredPartialCoverageSaysItOnce(t *testing.T) {
	h := newPartialCoverageHarness(t, WithApprovalGate(approvedGate("apr-2w")),
		WithProviderEraser(wiredProvider()), WithCryptoShredCoordinator(&stubCryptoShredCoordinator{}))
	tenant, owner := openTenant(t, h, "scanpartialw")

	r, status := runErasure(t, h, owner, tenant, "session", "sess-partial", "DSR-2W", nil)
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if got := jsonText(r.body["residual_scan_depth"]); got != wantDepth {
		t.Fatalf("wired residual_scan_depth = %q, want %q: %s", got, wantDepth, r.raw)
	}
	why := jsonText(r.body["verify_reason"])
	if n := strings.Count(why, wantCoverageMarker); n != 1 {
		t.Fatalf("the coverage marker appears %d times, want exactly 1: %q", n, why)
	}
	if !strings.Contains(why, wantPartialSentence) {
		t.Fatalf("wired verify_reason is not the same sentence:\n got: %q\nwant substring: %q", why, wantPartialSentence)
	}
	if r.body["verify_ok"] != false || status != erasureStatusGaps {
		t.Fatalf("wired verdict = %v / %q, want false / %q", r.body["verify_ok"], status, erasureStatusGaps)
	}
}

// ---- row 3: nothing opened -----------------------------------------------------------

// TestNothingOpenedIsDeclared is row 3u, and it is the live hole this change closes: a
// request whose data-class scope no target of the subject kind satisfies opens NOTHING,
// and before this change it sealed `completed`, verify_ok true — a clean bill of health
// from a scan that examined nothing. The create route accepts such a scope today.
func TestNothingOpenedIsDeclared(t *testing.T) {
	h := newHarness(t, WithApprovalGate(approvedGate("apr-3u")),
		WithProviderEraser(wiredProvider()), WithAccountEraser(wiredAccount()))
	tenant, owner := openTenant(t, h, "scanvacuous")

	r, status := runErasure(t, h, owner, tenant, "user", "maria@example.com", "DSR-3U", []string{classAuditLedger})
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if got, ok := r.body["residual_scan_depth"]; ok && jsonText(got) != "" {
		t.Fatalf("residual_scan_depth = %v, want ABSENT — nothing was opened, so no depth was reached: %s", got, r.raw)
	}
	if got := jsonStrings(r.body["residual_scan_opened"]); len(got) != 0 {
		t.Fatalf("residual_scan_opened = %v, want empty", got)
	}
	why := jsonText(r.body["verify_reason"])
	if !strings.Contains(why, wantUnestablishedSentence) {
		t.Fatalf("verify_reason does not carry the unestablished declaration verbatim.\n got: %q\nwant substring: %q", why, wantUnestablishedSentence)
	}
	if strings.Contains(why, wantCoverageMarker) {
		t.Fatalf("both scan declarations on one receipt: %q", why)
	}
	if r.body["verify_ok"] != false {
		t.Fatalf("verify_ok = %v, want false — an erasure that opened nothing is not a verified erasure: %s", r.body["verify_ok"], r.raw)
	}
	if status != erasureStatusGaps {
		t.Fatalf("status = %q, want %q", status, erasureStatusGaps)
	}
}

// TestWiredNothingOpenedSaysItOnce is row 3w: the same one sentence in the wired build.
func TestWiredNothingOpenedSaysItOnce(t *testing.T) {
	h := newHarness(t, WithApprovalGate(approvedGate("apr-3w")), WithProviderEraser(wiredProvider()),
		WithAccountEraser(wiredAccount()), WithCryptoShredCoordinator(&stubCryptoShredCoordinator{}))
	tenant, owner := openTenant(t, h, "scanvacuousw")

	r, status := runErasure(t, h, owner, tenant, "user", "maria@example.com", "DSR-3W", []string{classAuditLedger})
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	why := jsonText(r.body["verify_reason"])
	if n := strings.Count(why, wantDepthMarker); n != 1 {
		t.Fatalf("the unestablished marker appears %d times, want exactly 1: %q", n, why)
	}
	if !strings.Contains(why, wantUnestablishedSentence) {
		t.Fatalf("wired verify_reason is not the same sentence:\n got: %q\nwant substring: %q", why, wantUnestablishedSentence)
	}
	if got, ok := r.body["residual_scan_depth"]; ok && jsonText(got) != "" {
		t.Fatalf("wired residual_scan_depth = %v, want ABSENT", got)
	}
	if r.body["verify_ok"] != false || status != erasureStatusGaps {
		t.Fatalf("wired verdict = %v / %q, want false / %q", r.body["verify_ok"], status, erasureStatusGaps)
	}
}

// ---- rows 4 and 5: a scan that failed seals nothing -----------------------------------

// sealsNothingOnAScanFailure drives ONE build through a residual scan that could not
// finish, and asserts the whole of "no receipt, no claim": nothing sealed, nothing
// shredded, the request re-executable, and — when a coordinator is wired — a post-shred
// re-scan that was never even reached, because the run stopped two steps earlier. The
// re-execution then states what it opened, which is what makes the failure's silence a
// silence and not a lost claim.
//
// afterScan=false fails the scan's own read view before it opens anything; afterScan=true
// lets the scan's loop run first and fails the view it ran in.
func sealsNothingOnAScanFailure(t *testing.T, slug string, afterScan bool, coord *stubCryptoShredCoordinator) {
	t.Helper()
	provider := wiredProvider()
	opts := []Option{WithApprovalGate(approvedGate("apr-scanfail")), WithProviderEraser(provider)}
	if coord != nil {
		opts = append(opts, WithCryptoShredCoordinator(coord))
	}
	h := newHarness(t, opts...)
	tenant, owner := openTenant(t, h, slug)
	seedSubjectRows(h, tenant, "agent-fail")

	failing := &scanFailingData{inner: api.NewModuleData(h.st), afterScan: afterScan}
	h.mod.UseData(failing)
	// The provider leg is the last thing to run before the scan, so arming there fails
	// the scan's view and nothing earlier.
	provider.afterErase = func(context.Context, model.TenantID) error { failing.armed = true; return nil }

	hdr := tenantHdr(tenant)
	created := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "agent", "subject_ref": "agent-fail", "case_ref": "DSR-SCANFAIL",
	}, hdr)
	if created.code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.code, created.raw)
	}
	id := jsonText(created.body["id"])

	r := h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr)
	failing.armed = false
	if afterScan && !failing.scanRan {
		t.Fatal("the fixture did not reach the scan; this case would prove nothing")
	}
	if r.code != http.StatusInternalServerError {
		t.Fatalf("execute with a failed scan = %d %s, want 500", r.code, r.raw)
	}
	if rr := h.do("GET", "/v1/m/compliance/erasure/"+id+"/receipt", owner, nil, hdr); rr.code != http.StatusNotFound {
		t.Fatalf("a failed scan sealed a receipt: %d %s", rr.code, rr.raw)
	}
	if got := jsonText(h.do("GET", "/v1/m/compliance/erasure/"+id, owner, nil, hdr).body["status"]); got != erasureStatusFailed {
		t.Fatalf("status = %q, want %q", got, erasureStatusFailed)
	}
	if coord != nil && coord.verifyCalls != 0 {
		t.Fatalf("the post-shred re-scan ran %d times although the pre-shred scan never finished", coord.verifyCalls)
	}
	h.mutate(tenant, func(sc store.Scope) error {
		keys, err := sc.Ext(subjectKeyKind)
		if err != nil {
			return err
		}
		left, _, err := keys.List(context.Background(), model.Query{})
		if err != nil {
			return err
		}
		if len(left) == 0 {
			t.Fatal("the key died although no receipt was sealed; the key must outlive every retry")
		}
		return nil
	})

	// The retry is the point: the failure claimed nothing, and the run that succeeds
	// states what it opened.
	provider.afterErase = nil
	retry := h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr)
	if retry.code != http.StatusOK {
		t.Fatalf("re-execute = %d %s", retry.code, retry.raw)
	}
	if got := jsonText(retry.body["residual_scan_depth"]); got != wantDepth {
		t.Fatalf("the re-executed receipt's residual_scan_depth = %q, want %q: %s", got, wantDepth, retry.raw)
	}
	if got := jsonStrings(retry.body["residual_scan_opened"]); !sameStrings(got, wantAgentTargets) {
		t.Fatalf("the re-executed receipt's residual_scan_opened = %v, want %v", got, wantAgentTargets)
	}
}

// TestAFailedSourceScanSealsNoReceipt is rows 4u/4w: the scan's own read view could not
// be opened, so NOTHING was examined. The invariants range over SEALED receipts
// precisely because this path has none to range over: silence here is the honest
// answer, and it must stay silence rather than become an empty claim.
func TestAFailedSourceScanSealsNoReceipt(t *testing.T) {
	t.Run("un-wired", func(t *testing.T) { sealsNothingOnAScanFailure(t, "scanfail", false, nil) })
	t.Run("wired", func(t *testing.T) {
		sealsNothingOnAScanFailure(t, "scanfailw", false, &stubCryptoShredCoordinator{})
	})
}

// TestAMidScanFailureSealsNoReceipt is rows 5u/5w: the scan had already begun reading
// when the store failed under it. The outcome is the same as row 4 and that is the
// claim — a partially-completed scan is not a partial result, it is no result.
func TestAMidScanFailureSealsNoReceipt(t *testing.T) {
	t.Run("un-wired", func(t *testing.T) { sealsNothingOnAScanFailure(t, "scanmidfail", true, nil) })
	t.Run("wired", func(t *testing.T) {
		sealsNothingOnAScanFailure(t, "scanmidfailw", true, &stubCryptoShredCoordinator{})
	})
}

// ---- row 6: the post-shred re-scan ----------------------------------------------------

// TestUnwiredOwesNoReScan is row 6u. The post-shred re-scan is executed by the
// coordinator and by nothing else, so a build with no coordinator owes no re-scan and
// none can fail: no receipt it seals may carry a re-scan claim of any kind. The row is
// unreachable, and the unreachability is itself checkable.
func TestUnwiredOwesNoReScan(t *testing.T) {
	h := newHarness(t, WithApprovalGate(approvedGate("apr-6u")), WithProviderEraser(wiredProvider()))
	tenant, owner := openTenant(t, h, "scannorescan")
	seedSubjectRows(h, tenant, "agent-norescan")

	r, status := runErasure(t, h, owner, tenant, "agent", "agent-norescan", "DSR-6U", nil)
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if got := jsonText(r.body["residual_scan_depth"]); got != wantDepth {
		t.Fatalf("un-wired residual_scan_depth = %q, want %q — the depth comes from the pre-shred scan, not from a coordinator: %s", got, wantDepth, r.raw)
	}
	why := jsonText(r.body["verify_reason"])
	if strings.Contains(why, wantReScanMarker) || strings.Contains(why, "rescan") || strings.Contains(why, "coordinator") {
		t.Fatalf("an un-wired build made a re-scan claim: %q", why)
	}
	if r.body["verify_ok"] != true || status != erasureStatusCompleted {
		t.Fatalf("un-wired verdict = %v / %q, want true / %q: %s", r.body["verify_ok"], status, erasureStatusCompleted, r.raw)
	}
}

// TestAFailedReScanIsDeclaredWithoutTouchingTheDepth is row 6w, the case four earlier
// rounds could not seal honestly: the pre-shred scan SUCCEEDED and the post-shred
// re-scan FAILED. A depth is stated and the failure is declared, and no invariant
// breaks, because the two sentences are about two different scans and the second
// carries its own marker. The coordinator's half of the sentence is the overlay's; what
// this tree owns is that it reaches the receipt without disturbing the field.
func TestAFailedReScanIsDeclaredWithoutTouchingTheDepth(t *testing.T) {
	const reScanSentence = wantReScanMarker + ": the post-shred re-scan failed, so the scan was not " +
		"repeated after the key was destroyed"
	coord := &stubCryptoShredCoordinator{verify: CryptoShredVerification{
		Complete: false, KeyDestroyed: true, WORMNotified: true,
		Unverified: []string{reScanSentence}, PolicyApplied: "test",
	}}
	h := newHarness(t, WithApprovalGate(approvedGate("apr-6w")), WithProviderEraser(wiredProvider()),
		WithCryptoShredCoordinator(coord))
	tenant, owner := openTenant(t, h, "scanrescanfail")
	seedSubjectRows(h, tenant, "agent-rescan")

	r, status := runErasure(t, h, owner, tenant, "agent", "agent-rescan", "DSR-6W", nil)
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if got := jsonText(r.body["residual_scan_depth"]); got != wantDepth {
		t.Fatalf("a failed re-scan erased the pre-shred scan's depth: %q, want %q: %s", got, wantDepth, r.raw)
	}
	if got := jsonStrings(r.body["residual_scan_opened"]); !sameStrings(got, wantAgentTargets) {
		t.Fatalf("a failed re-scan disturbed residual_scan_opened: %v, want %v", got, wantAgentTargets)
	}
	why := jsonText(r.body["verify_reason"])
	if !strings.Contains(why, reScanSentence) {
		t.Fatalf("the re-scan failure is not declared: %q", why)
	}
	if strings.Contains(why, wantDepthMarker) || strings.Contains(why, wantCoverageMarker) {
		t.Fatalf("the re-scan's failure produced a SCAN declaration; the two are different claims: %q", why)
	}
	if r.body["verify_ok"] != false || status != erasureStatusGaps {
		t.Fatalf("verdict = %v / %q, want false / %q", r.body["verify_ok"], status, erasureStatusGaps)
	}
}

// ---- the invariants, as a table -------------------------------------------------------

// TestAReceiptNeverStatesADepthAndDeniesOne walks the three shapes a sealed receipt can
// have and asserts the two rules that make the field readable at all: NEVER BOTH (a
// stated depth beside a denial of one) and NEVER NEITHER (silence about what was
// examined). It also asserts the two scan markers are mutually exclusive, and that the
// verdict is exactly "the scan declared nothing".
func TestAReceiptNeverStatesADepthAndDeniesOne(t *testing.T) {
	cases := []struct {
		name       string
		partial    bool
		kind, ref  string
		classes    []string
		wantDepth  bool
		wantMarker string
	}{
		{name: "full coverage", kind: "agent", ref: "agent-inv", wantDepth: true},
		{name: "opened less than the scope required", partial: true, kind: "session", ref: "sess-inv",
			wantDepth: true, wantMarker: wantCoverageMarker},
		{name: "opened nothing", kind: "user", ref: "maria@example.com", classes: []string{classAuditLedger},
			wantMarker: wantDepthMarker},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := []Option{WithApprovalGate(approvedGate("apr-inv")), WithProviderEraser(wiredProvider()),
				WithAccountEraser(wiredAccount())}
			build := newHarness
			if tc.partial {
				build = newPartialCoverageHarness
			}
			h := build(t, opts...)
			tenant, owner := openTenant(t, h, "scaninv")
			r, status := runErasure(t, h, owner, tenant, tc.kind, tc.ref, "DSR-INV", tc.classes)
			if r.code != http.StatusOK {
				t.Fatalf("execute = %d %s", r.code, r.raw)
			}
			depth := jsonText(r.body["residual_scan_depth"])
			why := jsonText(r.body["verify_reason"])
			denied := strings.Contains(why, wantDepthMarker)

			if (depth != "") == denied {
				t.Fatalf("never both, never neither: depth = %q and the unestablished marker is %v (%s)", depth, denied, r.raw)
			}
			if (depth != "") != tc.wantDepth {
				t.Fatalf("depth stated = %v, want %v (%q)", depth != "", tc.wantDepth, depth)
			}
			if strings.Contains(why, wantDepthMarker) && strings.Contains(why, wantCoverageMarker) {
				t.Fatalf("the two scan declarations co-occur: %q", why)
			}
			if tc.wantMarker != "" && !strings.Contains(why, tc.wantMarker) {
				t.Fatalf("verify_reason = %q, want the %q declaration", why, tc.wantMarker)
			}
			// The property, not an option: any declaration flips the verdict.
			wantOK := tc.wantMarker == ""
			if r.body["verify_ok"] != wantOK {
				t.Fatalf("verify_ok = %v, want %v for %q", r.body["verify_ok"], wantOK, tc.name)
			}
			wantStatus := erasureStatusCompleted
			if !wantOK {
				wantStatus = erasureStatusGaps
			}
			if status != wantStatus {
				t.Fatalf("status = %q, want %q", status, wantStatus)
			}
		})
	}
}

// TestACoordinatorDepthNeverReachesTheReceipt is the other half of ONE SPEAKER, and it
// is about a coordinator this repository does not own: the public seam prints whatever
// a coordinator puts in its residual-scan summary, and a third party written to the
// older contract sets a depth there unconditionally. Printed beside the typed field, it
// is a second depth on one receipt — the defect, restated by an embedder. The Community
// must not print it at all.
func TestACoordinatorDepthNeverReachesTheReceipt(t *testing.T) {
	coord := &stubCryptoShredCoordinator{verify: CryptoShredVerification{
		Complete: false, KeyDestroyed: true, WORMNotified: true,
		ResidualScan:  CryptoShredResidualScan{ScanDepth: "deep", TargetsScanned: 99, Clean: true},
		Unverified:    []string{"worm notification unrecorded for key (test)"},
		PolicyApplied: "third-party",
	}}
	h := newHarness(t, WithApprovalGate(approvedGate("apr-1spk")), WithProviderEraser(wiredProvider()),
		WithCryptoShredCoordinator(coord))
	tenant, owner := openTenant(t, h, "scanonespeaker")
	seedSubjectRows(h, tenant, "agent-onespeaker")

	r, _ := runErasure(t, h, owner, tenant, "agent", "agent-onespeaker", "DSR-1SPK", nil)
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	why := jsonText(r.body["verify_reason"])
	if strings.Contains(why, "scan_depth=") || strings.Contains(why, "deep") {
		t.Fatalf("a coordinator's depth reached the receipt beside the typed field: %q", why)
	}
	if !strings.Contains(why, "worm notification unrecorded for key (test)") {
		t.Fatalf("the coordinator's own unverified claim was dropped: %q", why)
	}
	if got := jsonText(r.body["residual_scan_depth"]); got != wantDepth {
		t.Fatalf("residual_scan_depth = %q, want %q (the scan's own, unaffected)", got, wantDepth)
	}
}

// ---- the manifest ----------------------------------------------------------------------

// manifestCandidate rebuilds the canonical manifest string from the receipt the engine
// returned, in the layout the seal is expected to write. It is a deliberate second
// implementation of ONE wire format — the bytes the tamper-evidence hash commits to —
// so a silent change to that format fails here instead of invalidating every stored
// hash. withScan=false rebuilds the format as it was BEFORE this change, which is what
// makes "the labels are covered" a real assertion rather than a tautology.
func manifestCandidate(body map[string]any, withScan bool) string {
	version := "erasure-receipt|v2"
	if !withScan {
		version = "erasure-receipt|v1"
	}
	parts := []string{version, jsonText(body["erasure_id"]), jsonText(body["subject_kind"]),
		strings.Join(jsonStrings(body["data_classes"]), ",")}
	for _, raw := range body["targets"].([]any) {
		o, _ := raw.(map[string]any)
		parts = append(parts, jsonText(o["target"])+":"+jsonText(o["mode"])+":"+
			itoa(int64(intOf(o["examined"])))+":"+itoa(int64(intOf(o["erased"])))+":"+
			itoa(int64(intOf(o["scrubbed"])))+":"+jsonText(o["status"]))
	}
	if withScan {
		// The residues list is empty on every clean run, which is the only shape this
		// helper is used on.
		parts = append(parts, "scan:"+jsonText(body["residual_scan_depth"])+":"+
			strings.Join(jsonStrings(body["residual_scan_opened"]), ",")+":"+
			strings.Join(jsonStrings(body["residual_scan_applicable"]), ",")+":")
	}
	parts = append(parts, jsonText(body["account_outcome"]), jsonText(body["provider_outcome"]))
	if body["verify_ok"] == true {
		parts = append(parts, "verify:ok")
	} else {
		parts = append(parts, "verify:failed")
	}
	return strings.Join(parts, "|")
}

// TestErasureManifestV2CommitsToTheLabels: the receipt's tamper-evidence hash must
// cover WHAT THE SCAN OPENED, not a count of it and not nothing at all. A manifest that
// still hashed the pre-change layout would let the two label sets be edited in the
// database without breaking the hash a third party verifies.
func TestErasureManifestV2CommitsToTheLabels(t *testing.T) {
	check := func(t *testing.T, r resp) {
		t.Helper()
		got := jsonText(r.body["manifest_hash"])
		if why := jsonText(r.body["verify_reason"]); strings.Contains(why, "residual identifiers at:") {
			t.Fatalf("this fixture must be a clean run; verify_reason = %q", why)
		}
		if want := hashHex(manifestCandidate(r.body, true)); got != want {
			t.Fatalf("manifest_hash = %s, want %s\nover: %q", got, want, manifestCandidate(r.body, true))
		}
		if old := hashHex(manifestCandidate(r.body, false)); got == old {
			t.Fatal("the manifest still commits to the pre-change layout: the scan's labels are outside the hash")
		}
	}

	t.Run("full coverage", func(t *testing.T) {
		h := newHarness(t, WithApprovalGate(approvedGate("apr-mf")), WithProviderEraser(wiredProvider()))
		tenant, owner := openTenant(t, h, "scanmanifest")
		seedSubjectRows(h, tenant, "agent-manifest")
		r, _ := runErasure(t, h, owner, tenant, "agent", "agent-manifest", "DSR-MF", nil)
		if r.code != http.StatusOK {
			t.Fatalf("execute = %d %s", r.code, r.raw)
		}
		check(t, r)
	})

	t.Run("partial coverage", func(t *testing.T) {
		h := newPartialCoverageHarness(t, WithApprovalGate(approvedGate("apr-mp")), WithProviderEraser(wiredProvider()))
		tenant, owner := openTenant(t, h, "scanmanifestp")
		r, _ := runErasure(t, h, owner, tenant, "session", "sess-manifest", "DSR-MP", nil)
		if r.code != http.StatusOK {
			t.Fatalf("execute = %d %s", r.code, r.raw)
		}
		check(t, r)
	})
}

// TestAReceiptSealedBeforeThisChangeKeepsNullFieldsAndItsHash is the append-only rule,
// stated as a test: the receipt table gains four nullable columns and NOTHING touches a
// row already in it. A receipt sealed before this change reads back with those fields
// absent and with the manifest hash it was sealed under — never recomputed under the
// new layout, which would turn every stored receipt into a mismatch.
func TestAReceiptSealedBeforeThisChangeKeepsNullFieldsAndItsHash(t *testing.T) {
	h := newHarness(t, WithApprovalGate(approvedGate("apr-old")), WithProviderEraser(wiredProvider()))
	tenant, owner := openTenant(t, h, "scanoldreceipt")
	hdr := tenantHdr(tenant)

	oldID := model.NewID().String()
	oldHash := hashHex("erasure-receipt|v1|a receipt sealed before this change")
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(erasureReceiptKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			colRCErasureID:  oldID,
			colRCSubject:    "agent",
			colRCToken:      "pii-token-already-dead",
			colRCTargets:    encodeJSON([]targetOutcome{{Target: "knowledge.memory", Mode: eraseModeDelete, Status: "ok"}}),
			colRCAccount:    "not applicable to subject kind agent",
			colRCProvider:   "not_wired",
			colRCFloorDays:  int64(0),
			colRCFloorKnown: false,
			colRCShredded:   true,
			colRCVerifyOK:   true,
			colRCVerifyN:    int64(4),
			colRCRetained:   encodeJSON(retainedReconciliation),
			colCaseRef:      "DSR-OLD",
			colApprovalRef:  "apr-old",
			colLedgerSeq:    int64(4),
			colManifestHash: oldHash,
		})
		return err
	})

	old := h.do("GET", "/v1/m/compliance/erasure/"+oldID+"/receipt", owner, nil, hdr)
	if old.code != http.StatusOK {
		t.Fatalf("reading a pre-change receipt = %d %s", old.code, old.raw)
	}
	for _, field := range []string{"residual_scan_depth", "residual_scan_opened", "residual_scan_applicable", "data_classes"} {
		if v, ok := old.body[field]; ok {
			t.Fatalf("a pre-change receipt grew %s = %v; the new columns are NULL on every existing row", field, v)
		}
	}
	if got := jsonText(old.body["manifest_hash"]); got != oldHash {
		t.Fatalf("manifest_hash = %s, want the stored %s — a stored receipt was re-hashed", got, oldHash)
	}

	// And the receipt sealed AFTER the change does carry the fields, in the same table.
	seedSubjectRows(h, tenant, "agent-new")
	r, _ := runErasure(t, h, owner, tenant, "agent", "agent-new", "DSR-NEW", nil)
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if got := jsonText(r.body["residual_scan_depth"]); got != wantDepth {
		t.Fatalf("a receipt sealed after the change = %q, want %q: %s", got, wantDepth, r.raw)
	}
	if again := h.do("GET", "/v1/m/compliance/erasure/"+oldID+"/receipt", owner, nil, hdr); jsonText(again.body["manifest_hash"]) != oldHash {
		t.Fatalf("sealing a new receipt disturbed the old one's hash: %s", again.raw)
	}
}

// ---- the seam an out-of-tree coordinator sees --------------------------------------------

// legacyProbes is the probe struct of the PREVIOUS contract, as an out-of-tree
// coordinator would have copied it: the residual-scan probe returns a residue list and
// a COUNT of targets, which is exactly the shape that could not say what it opened.
type legacyProbes struct {
	KeyGone      func(ctx context.Context) (bool, error)
	ResidualScan func(ctx context.Context) ([]string, int, error)
}

// legacyVerification is the verdict such a coordinator returns, reflected field by field.
type legacyVerification struct {
	Complete      bool
	KeyDestroyed  bool
	WORMNotified  bool
	Unverified    []string
	PolicyApplied string
}

// reflectLegacyCoordinator reads the probes by NAME, the way an AGPL embedder that
// cannot import this package must. After the rename it finds no probe where it looks.
type reflectLegacyCoordinator struct{ sawProbe bool }

func (c *reflectLegacyCoordinator) ValidateShredReadiness(context.Context, string, string, string) (*reflectShredReadiness, error) {
	return &reflectShredReadiness{Ready: true, PolicyApplied: "legacy"}, nil
}

func (c *reflectLegacyCoordinator) NotifyWORMSinks(context.Context, string, time.Time) error {
	return nil
}

func (c *reflectLegacyCoordinator) VerifyShredCompleteness(_ context.Context, _ string, _ []string, probes any) (*legacyVerification, error) {
	f := reflect.ValueOf(probes).FieldByName("ResidualScan")
	if !f.IsValid() || (f.Kind() == reflect.Func && f.IsNil()) {
		return &legacyVerification{
			KeyDestroyed: true, WORMNotified: true,
			Unverified:    []string{"residual scan probe absent at the seam"},
			PolicyApplied: "legacy",
		}, nil
	}
	c.sawProbe = true
	return &legacyVerification{Complete: true, KeyDestroyed: true, WORMNotified: true, PolicyApplied: "legacy"}, nil
}

// typedLegacyCoordinator declares the probe STRUCT of the previous contract in its own
// signature, so the module's adapter cannot hand it the current one.
type typedLegacyCoordinator struct{}

func (typedLegacyCoordinator) ValidateShredReadiness(context.Context, string, string, string) (*reflectShredReadiness, error) {
	return &reflectShredReadiness{Ready: true, PolicyApplied: "legacy"}, nil
}

func (typedLegacyCoordinator) NotifyWORMSinks(context.Context, string, time.Time) error { return nil }

func (typedLegacyCoordinator) VerifyShredCompleteness(context.Context, string, []string, legacyProbes) (*legacyVerification, error) {
	return &legacyVerification{Complete: true, KeyDestroyed: true, WORMNotified: true}, nil
}

// TestAnOldShapeCoordinatorFailsClosedAtTheRenamedSeam covers both ways an out-of-tree
// coordinator written to the previous contract meets the new one. Neither may end in a
// receipt that says the shred was verified when it was not: one becomes a DECLARED gap,
// the other an error that names the seam and seals nothing.
func TestAnOldShapeCoordinatorFailsClosedAtTheRenamedSeam(t *testing.T) {
	t.Run("a probe read by name is absent, and that is declared", func(t *testing.T) {
		coord := &reflectLegacyCoordinator{}
		h := newHarness(t, WithApprovalGate(approvedGate("apr-lg1")), WithProviderEraser(wiredProvider()),
			WithCryptoShredCoordinator(coord))
		tenant, owner := openTenant(t, h, "scanlegacyname")
		seedSubjectRows(h, tenant, "agent-legacy")

		r, status := runErasure(t, h, owner, tenant, "agent", "agent-legacy", "DSR-LG1", nil)
		if r.code != http.StatusOK {
			t.Fatalf("execute = %d %s", r.code, r.raw)
		}
		if coord.sawProbe {
			t.Fatal("the old field name still resolves; an out-of-tree coordinator would keep calling a probe that no longer answers")
		}
		why := jsonText(r.body["verify_reason"])
		if !strings.Contains(why, "residual scan probe absent at the seam") {
			t.Fatalf("the coordinator's declared gap did not reach the receipt: %q", why)
		}
		if r.body["verify_ok"] != false || status != erasureStatusGaps {
			t.Fatalf("verdict = %v / %q, want false / %q — an unverifiable shred is a gap", r.body["verify_ok"], status, erasureStatusGaps)
		}
		// The Community's own statement is unaffected: it never needed the coordinator.
		if got := jsonText(r.body["residual_scan_depth"]); got != wantDepth {
			t.Fatalf("residual_scan_depth = %q, want %q", got, wantDepth)
		}
	})

	t.Run("a probe struct of the old type fails closed inside the shred transaction", func(t *testing.T) {
		h := newHarness(t, WithApprovalGate(approvedGate("apr-lg2")), WithProviderEraser(wiredProvider()),
			WithCryptoShredCoordinator(typedLegacyCoordinator{}))
		tenant, owner := openTenant(t, h, "scanlegacytype")
		seedSubjectRows(h, tenant, "agent-legacy2")
		hdr := tenantHdr(tenant)

		created := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
			"subject_kind": "agent", "subject_ref": "agent-legacy2", "case_ref": "DSR-LG2",
		}, hdr)
		if created.code != http.StatusCreated {
			t.Fatalf("create = %d %s", created.code, created.raw)
		}
		id := jsonText(created.body["id"])
		r := h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr)
		if r.code != http.StatusInternalServerError {
			t.Fatalf("execute = %d %s, want 500 (fail closed)", r.code, r.raw)
		}
		if !strings.Contains(r.raw, "VerifyShredCompleteness") {
			t.Fatalf("the refusal does not name the seam it refused at: %s", r.raw)
		}
		if rr := h.do("GET", "/v1/m/compliance/erasure/"+id+"/receipt", owner, nil, hdr); rr.code != http.StatusNotFound {
			t.Fatalf("a refused verification sealed a receipt: %d %s", rr.code, rr.raw)
		}
		h.mutate(tenant, func(sc store.Scope) error {
			keys, err := sc.Ext(subjectKeyKind)
			if err != nil {
				return err
			}
			left, _, err := keys.List(context.Background(), model.Query{})
			if err != nil {
				return err
			}
			if len(left) == 0 {
				t.Fatal("the key died although the verification was refused")
			}
			return nil
		})
	})
}

// ---- the labels themselves -----------------------------------------------------------------

// TestScanTargetLabelsAreStructuralOnly: the two label sets travel on a certificate that
// OUTLIVES the subject's key, so a label that could carry a personal datum, an
// identifier or a tenant-specific name would re-identify a person the erasure was
// supposed to make unreachable. Every label must come from the fixed, in-code catalog.
func TestScanTargetLabelsAreStructuralOnly(t *testing.T) {
	allowed := map[string]bool{
		// The three targets the same scan walks outside the registry.
		"knowledge.document": true, "core.identities": true, "core.cost_records": true,
	}
	for _, target := range erasureTargetRegistry {
		allowed[target.Label] = true
	}

	h := newHarness(t, WithApprovalGate(approvedGate("apr-lbl")), WithProviderEraser(wiredProvider()),
		WithAccountEraser(wiredAccount()))
	tenant, owner := openTenant(t, h, "scanlabels")
	const subject = "maria.perez@example.com"
	h.mutate(tenant, func(sc store.Scope) error {
		samples, err := sc.Ext(costSampleStandInKind)
		if err != nil {
			return err
		}
		_, err = samples.Create(context.Background(), model.Record{
			"input_tokens": int64(10), "cost_micro_usd": int64(5), "actor": subject,
		})
		return err
	})

	r, _ := runErasure(t, h, owner, tenant, "user", subject, "DSR-LBL", nil)
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	labels := append(jsonStrings(r.body["residual_scan_opened"]), jsonStrings(r.body["residual_scan_applicable"])...)
	if len(labels) == 0 {
		t.Fatalf("the receipt carries no labels to check: %s", r.raw)
	}
	for _, label := range labels {
		if !allowed[label] {
			t.Fatalf("label %q is not one of the catalog's structural labels", label)
		}
		if strings.Contains(label, subject) || strings.Contains(label, "maria") || strings.Contains(label, "@") {
			t.Fatalf("label %q carries a personal datum", label)
		}
		if strings.Contains(label, tenant.String()) {
			t.Fatalf("label %q carries the tenant id", label)
		}
	}
}

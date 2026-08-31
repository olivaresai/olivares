// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// digest returns a canonical lowercase-hex SHA-256 of s, the shape a presented
// ContentDigest must have (invariant #3; enforced by validatePresentedRequest).
func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// --- fixture -----------------------------------------------------------------

// delegFixture extends the PEP fixture with a governed SUBJECT (a real user
// session), two PEP services in the same tenant (for confused-deputy proofs),
// and their authenticated PEP identities.
type delegFixture struct {
	pepFixture
	subjectSession   string
	subjectSessionID model.ID
	subjectUserID    model.ID
	subjectExpiry    model.Timestamp
	serviceA         model.PEPService
	serviceB         model.PEPService
	pepA             auth.PEPIdentity
	pepB             auth.PEPIdentity
	groupID          model.ID
}

const subjectPassword = "subject-password-1"

func newDelegFixture(t *testing.T) delegFixture {
	return newDelegFixtureFromPEP(t, newPEPFixture(t))
}

// newDelegFixtureFromStore provisions the delegation fixture on a caller-supplied
// store, so a test can exercise the claim/finalize paths against a store with a
// non-default audit-spool policy (a tiny DEGRADE budget for evidence-drop tests).
func newDelegFixtureFromStore(t *testing.T, st store.Store) delegFixture {
	return newDelegFixtureFromPEP(t, newPEPFixtureFromStore(t, st))
}

func newDelegFixtureFromPEP(t *testing.T, pf pepFixture) delegFixture {
	t.Helper()

	u, err := pf.a.CreateUser(pf.ctx, pf.super, auth.NewUser{
		Email: "subject@acme.test", DisplayName: "Subject", Password: subjectPassword,
	})
	if err != nil {
		t.Fatalf("create subject: %v", err)
	}
	if _, err := pf.a.GrantMembership(pf.ctx, pf.super, u.ID, pf.tenant, auth.RoleEditor, model.ID("")); err != nil {
		t.Fatalf("grant subject membership: %v", err)
	}
	groupID := createGroupWithMember(t, pf.ctx, pf.st, pf.tenant, u.ID)

	session, sess, err := pf.a.Login(pf.ctx, "subject@acme.test", subjectPassword, "127.0.0.1")
	if err != nil {
		t.Fatalf("login subject: %v", err)
	}

	serviceA := pf.register(t, "deleg-pep-a", testPDPAudience, map[string]bool{
		"buffer_request": true, "streaming": true,
	})
	serviceB := pf.register(t, "deleg-pep-b", testPDPAudience, map[string]bool{
		"buffer_request": true,
	})

	return delegFixture{
		pepFixture:       pf,
		subjectSession:   session,
		subjectSessionID: sess.ID,
		subjectUserID:    u.ID,
		subjectExpiry:    sess.ExpiresAt,
		serviceA:         serviceA,
		serviceB:         serviceB,
		pepA:             authPEP(t, pf, serviceA),
		pepB:             authPEP(t, pf, serviceB),
		groupID:          groupID,
	}
}

func authPEP(t *testing.T, pf pepFixture, service model.PEPService) auth.PEPIdentity {
	t.Helper()
	bearer, _ := pf.bind(t, service, testPDPAudience)
	id, err := pf.a.AuthenticatePEP(pf.ctx, bearer)
	if err != nil {
		t.Fatalf("authenticate PEP %q: %v", service.Name, err)
	}
	return id
}

func createGroupWithMember(t *testing.T, ctx context.Context, st store.Store, tenant model.TenantID, userID model.ID) model.ID {
	t.Helper()
	var gid model.ID
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		g, err := as.Groups().Create(ctx, model.UserGroup{TargetTenantID: tenant, DisplayName: "Engineering"})
		if err != nil {
			return err
		}
		gid = g.ID
		_, err = as.GroupMembers().Create(ctx, model.UserGroupMember{GroupID: g.ID, UserID: userID})
		return err
	}); err != nil {
		t.Fatalf("create group with member: %v", err)
	}
	return gid
}

func (f delegFixture) mint(t *testing.T, service model.PEPService, ops ...string) (string, model.DelegationHandle) {
	t.Helper()
	if len(ops) == 0 {
		ops = []string{"messages"}
	}
	token, stored, err := f.a.MintDelegationHandle(f.ctx, f.admin, auth.MintDelegationRequest{
		SubjectToken: f.subjectSession, PEPServiceID: service.ID, Operations: ops,
	})
	if err != nil {
		t.Fatalf("mint delegation handle: %v", err)
	}
	return token, stored
}

func handleProof(token string) auth.DelegationProofInput {
	return auth.DelegationProofInput{Scheme: "handle", Token: []byte(token)}
}

func freshPresented(nonce, op string) auth.PresentedRequest {
	return auth.PresentedRequest{
		Nonce:                nonce,
		OperationKind:        op,
		Model:                "claude-opus-x",
		Stream:               false,
		ContentDigest:        digest("content-abc"),
		ContentSize:          42,
		MediaType:            "application/json",
		IssuedAt:             time.Now(),
		DeclaredCapabilities: map[string]bool{"buffer_request": true},
	}
}

func (f delegFixture) getHandle(t *testing.T, id model.ID) (model.DelegationHandle, error) {
	t.Helper()
	var h model.DelegationHandle
	var gerr error
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		h, gerr = as.DelegationHandles().Get(f.ctx, id)
		return nil
	}); err != nil {
		t.Fatalf("view handle: %v", err)
	}
	return h, gerr
}

// --- Mint --------------------------------------------------------------------

func TestMintDelegationHappyPathStoresCeilingAndAudits(t *testing.T) {
	f := newDelegFixture(t)
	token, stored := f.mint(t, f.serviceA, "messages", "messages_batch")

	if !strings.HasPrefix(token, auth.PrefixDelegation+"_") {
		t.Errorf("minted token prefix = %q, want olvd_", token)
	}
	if stored.TargetTenantID != f.tenant {
		t.Errorf("handle TargetTenantID = %q, want %q", stored.TargetTenantID, f.tenant)
	}
	if stored.SourceCredKind != "user" || stored.SourceCredID != f.subjectSessionID {
		t.Errorf("handle source = %q/%q, want user/%q", stored.SourceCredKind, stored.SourceCredID, f.subjectSessionID)
	}
	if stored.SubjectUserID != f.subjectUserID {
		t.Errorf("handle SubjectUserID = %q, want %q", stored.SubjectUserID, f.subjectUserID)
	}
	if stored.MintRole != auth.RoleEditor {
		t.Errorf("handle MintRole = %q, want editor", stored.MintRole)
	}
	if len(stored.MintGroups) != 1 || stored.MintGroups[0] != f.groupID.String() {
		t.Errorf("handle MintGroups = %v, want [%s]", stored.MintGroups, f.groupID)
	}
	if stored.PEPServiceID != f.serviceA.ID || stored.Audience != f.serviceA.PDPAudience {
		t.Errorf("handle service/audience = %q/%q", stored.PEPServiceID, stored.Audience)
	}
	if len(stored.Operations) != 2 {
		t.Errorf("handle Operations = %v, want two", stored.Operations)
	}
	assertPEPAuditActions(t, f.pepFixture, "delegation.mint")
}

func TestMintDelegationRefusals(t *testing.T) {
	f := newDelegFixture(t)

	// superadmin subject: down-scoping a system token is refused.
	superTok, _, err := f.a.IssueToken(f.ctx, f.super, auth.TokenSpec{Name: "sys", Superadmin: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.a.MintDelegationHandle(f.ctx, f.admin, auth.MintDelegationRequest{
		SubjectToken: superTok, PEPServiceID: f.serviceA.ID, Operations: []string{"messages"},
	}); !errors.Is(err, auth.ErrInvalidDelegationRequest) {
		t.Errorf("superadmin subject err = %v, want ErrInvalidDelegationRequest", err)
	}

	// empty operations.
	if _, _, err := f.a.MintDelegationHandle(f.ctx, f.admin, auth.MintDelegationRequest{
		SubjectToken: f.subjectSession, PEPServiceID: f.serviceA.ID, Operations: nil,
	}); !errors.Is(err, auth.ErrInvalidDelegationRequest) {
		t.Errorf("empty operations err = %v, want ErrInvalidDelegationRequest", err)
	}

	// unknown operation kind.
	if _, _, err := f.a.MintDelegationHandle(f.ctx, f.admin, auth.MintDelegationRequest{
		SubjectToken: f.subjectSession, PEPServiceID: f.serviceA.ID, Operations: []string{"delete_everything"},
	}); !errors.Is(err, auth.ErrInvalidDelegationRequest) {
		t.Errorf("unknown operation err = %v, want ErrInvalidDelegationRequest", err)
	}

	// missing PEP service.
	if _, _, err := f.a.MintDelegationHandle(f.ctx, f.admin, auth.MintDelegationRequest{
		SubjectToken: f.subjectSession, PEPServiceID: model.NewID(), Operations: []string{"messages"},
	}); !errors.Is(err, auth.ErrDelegationPEPService) {
		t.Errorf("missing PEP service err = %v, want ErrDelegationPEPService", err)
	}

	// cross-tenant PEP service: register one in the OTHER tenant, mint for a same-tenant subject.
	otherService, err := f.a.RegisterPEPService(f.ctx, f.super, auth.PEPServiceSpec{
		Tenant: f.other, Name: "cross", PDPAudience: testPDPAudience,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.a.MintDelegationHandle(f.ctx, f.admin, auth.MintDelegationRequest{
		SubjectToken: f.subjectSession, PEPServiceID: otherService.ID, Operations: []string{"messages"},
	}); !errors.Is(err, auth.ErrDelegationPEPService) {
		t.Errorf("cross-tenant PEP service err = %v, want ErrDelegationPEPService", err)
	}

	// disabled PEP service.
	if err := f.a.DisablePEPService(f.ctx, f.admin, f.serviceA.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.a.MintDelegationHandle(f.ctx, f.admin, auth.MintDelegationRequest{
		SubjectToken: f.subjectSession, PEPServiceID: f.serviceA.ID, Operations: []string{"messages"},
	}); !errors.Is(err, auth.ErrDelegationPEPService) {
		t.Errorf("disabled PEP service err = %v, want ErrDelegationPEPService", err)
	}
}

func TestMintDelegationClampsTTLToSubjectExpiry(t *testing.T) {
	f := newDelegFixture(t)
	// Configure a max TTL far beyond the 12h session, so the SUBJECT-credential
	// expiry (12h) is the binding clamp (the configured max no longer shortens it).
	f.a.SetDelegationPolicy(240*time.Hour, 0, 0)
	_, stored, err := f.a.MintDelegationHandle(f.ctx, f.admin, auth.MintDelegationRequest{
		SubjectToken: f.subjectSession, PEPServiceID: f.serviceA.ID,
		Operations: []string{"messages"}, // no override → configured 240h, clamped to the 12h session
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if stored.ExpiresAt.String() != f.subjectExpiry.String() {
		t.Errorf("handle expiry = %s, want clamped to subject expiry %s", stored.ExpiresAt, f.subjectExpiry)
	}
}

func TestMintConfinedSubjectRefused(t *testing.T) {
	f := newDelegFixture(t)
	// Create a workspace-confined subject: a membership scoped to a workspace id.
	confined, err := f.a.CreateUser(f.ctx, f.super, auth.NewUser{Email: "confined@acme.test", Password: subjectPassword})
	if err != nil {
		t.Fatal(err)
	}
	ws := model.NewID()
	if _, err := f.a.GrantMembership(f.ctx, f.super, confined.ID, f.tenant, auth.RoleEditor, ws); err != nil {
		t.Fatal(err)
	}
	sess, _, err := f.a.Login(f.ctx, "confined@acme.test", subjectPassword, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.a.MintDelegationHandle(f.ctx, f.admin, auth.MintDelegationRequest{
		SubjectToken: sess, PEPServiceID: f.serviceA.ID, Operations: []string{"messages"},
	}); !errors.Is(err, auth.ErrWorkspaceConfined) {
		t.Errorf("confined subject mint err = %v, want ErrWorkspaceConfined", err)
	}
}

// --- Parse / Scheme ----------------------------------------------------------

func TestVerifyParseAndSchemeErrorsAreProtocol(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA)

	cases := []struct {
		name  string
		proof auth.DelegationProofInput
	}{
		{"empty scheme", auth.DelegationProofInput{Scheme: "", Token: []byte(token)}},
		{"unknown scheme", auth.DelegationProofInput{Scheme: "mystery", Token: []byte(token)}},
		{"case variant scheme", auth.DelegationProofInput{Scheme: "Handle", Token: []byte(token)}},
		{"reserved actas-token", auth.DelegationProofInput{Scheme: "actas-token", Token: []byte(token)}},
		{"empty token", handleProof("")},
		{"wrong prefix", handleProof("olvk_" + token[5:])},
		{"bad alphabet", handleProof("olvd_sel$ector_secret")},
		{"extra separators", handleProof("olvd_sel_sec_extra")},
		{"oversized", handleProof("olvd_" + strings.Repeat("A", 4096) + "_" + strings.Repeat("B", 4096))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, tc.proof, freshPresented("n-"+tc.name, "messages"))
			if !errors.Is(err, auth.ErrDelegationProtocol) {
				t.Fatalf("err = %v, want ErrDelegationProtocol", err)
			}
		})
	}
}

// TestVerifyRejectsMalformedPresentedRequest pins H1: a presented request missing
// an essential binding (empty/oversized nonce, empty model, negative content size,
// non-canonical content digest) is a protocol error BEFORE any claim is taken, so
// a PEP cannot register a sealed claim over an under-specified request whose
// fields all hash to a fixed value.
func TestVerifyRejectsMalformedPresentedRequest(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA)

	cases := []struct {
		name   string
		mutate func(*auth.PresentedRequest)
	}{
		{"empty nonce", func(pr *auth.PresentedRequest) { pr.Nonce = "" }},
		{"oversized nonce", func(pr *auth.PresentedRequest) { pr.Nonce = strings.Repeat("n", 513) }},
		{"empty model", func(pr *auth.PresentedRequest) { pr.Model = "" }},
		{"negative content size", func(pr *auth.PresentedRequest) { pr.ContentSize = -1 }},
		{"non-hex digest", func(pr *auth.PresentedRequest) { pr.ContentDigest = "not-a-canonical-sha256-digest-value" }},
		{"prefixed digest", func(pr *auth.PresentedRequest) { pr.ContentDigest = "sha256:" + digest("x") }},
		{"short digest", func(pr *auth.PresentedRequest) { pr.ContentDigest = digest("x")[:32] }},
		{"uppercase digest", func(pr *auth.PresentedRequest) { pr.ContentDigest = strings.ToUpper(digest("x")) }},
		{"empty digest", func(pr *auth.PresentedRequest) { pr.ContentDigest = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := freshPresented("nonce-malformed-"+tc.name, "messages")
			tc.mutate(&pr)
			if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr); !errors.Is(err, auth.ErrDelegationProtocol) {
				t.Fatalf("err = %v, want ErrDelegationProtocol", err)
			}
		})
	}

	// No malformed attempt consumed the single-use handle: a fresh well-formed
	// claim on the same handle still succeeds (nothing was claimed).
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("nonce-clean", "messages")); err != nil {
		t.Fatalf("clean claim after malformed attempts: %v", err)
	}
}

// --- Verify happy path + principal construction ------------------------------

func TestVerifyAndClaimHappyPathAndPrincipal(t *testing.T) {
	f := newDelegFixture(t)
	token, stored := f.mint(t, f.serviceA)

	v, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("nonce-1", "messages"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if v.DecisionID().IsZero() {
		t.Error("DecisionID is zero")
	}
	if v.Retried() {
		t.Error("first verify must not be a retry")
	}
	if v.Tenant() != f.tenant || v.SubjectUserID() != f.subjectUserID {
		t.Errorf("tenant/subject = %q/%q", v.Tenant(), v.SubjectUserID())
	}
	if v.EffectiveRole() != auth.RoleEditor {
		t.Errorf("effective role = %q, want editor", v.EffectiveRole())
	}
	if len(v.EffectiveGroups()) != 1 || v.EffectiveGroups()[0] != f.groupID.String() {
		t.Errorf("effective groups = %v, want [%s]", v.EffectiveGroups(), f.groupID)
	}
	if v.PEPServiceID() != f.serviceA.ID {
		t.Errorf("pep service = %q, want %q", v.PEPServiceID(), f.serviceA.ID)
	}
	if !v.EffectiveCapabilities()["buffer_request"] || v.CapabilityVersion() != 1 {
		t.Errorf("effective caps = %v v%d", v.EffectiveCapabilities(), v.CapabilityVersion())
	}

	// The gated principal names the SUBJECT, never the handle or the PEP.
	p := auth.PrincipalForDelegation(v)
	if p.Kind != auth.KindUser || p.UserID != f.subjectUserID {
		t.Errorf("principal = {kind %s, user %s}, want {user, %s}", p.Kind, p.UserID, f.subjectUserID)
	}
	if p.CredID == model.ID(stored.ID) {
		t.Error("principal CredID must not be the handle JTI")
	}
	if p.Superadmin || p.AAL != 0 || p.AMR != nil {
		t.Errorf("principal superadmin/AAL/AMR = %v/%d/%v, want false/0/nil", p.Superadmin, p.AAL, p.AMR)
	}
	if r, _ := p.RoleIn(f.tenant); r != auth.RoleEditor {
		t.Errorf("principal role = %q, want editor", r)
	}
	if g := p.GroupsIn(f.tenant); len(g) != 1 || g[0] != f.groupID.String() {
		t.Errorf("principal groups = %v, want [%s]", g, f.groupID)
	}
}

// --- Verify refusals: confused deputy, tenant, audience, scope, digest --------

func TestVerifyConfusedDeputyDeniesCrossService(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA) // minted for PEP-A

	// Presented by the authenticated PEP-B.
	_, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepB, handleProof(token), freshPresented("nonce-cd", "messages"))
	if !errors.Is(err, auth.ErrDelegationInvalid) {
		t.Fatalf("confused-deputy err = %v, want ErrDelegationInvalid", err)
	}
}

func TestVerifyOperationOutsideScopeDenied(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA, "messages") // only "messages"
	_, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("nonce-op", "messages_batch"))
	if !errors.Is(err, auth.ErrDelegationInvalid) {
		t.Fatalf("operation-out-of-scope err = %v, want ErrDelegationInvalid", err)
	}
}

func TestVerifyBoundDigestMismatchDenied(t *testing.T) {
	f := newDelegFixture(t)
	token, _, err := f.a.MintDelegationHandle(f.ctx, f.admin, auth.MintDelegationRequest{
		SubjectToken: f.subjectSession, PEPServiceID: f.serviceA.ID,
		Operations: []string{"messages"}, BoundDigest: digest("bound-to-this"),
	})
	if err != nil {
		t.Fatal(err)
	}
	pr := freshPresented("nonce-bd", "messages")
	pr.ContentDigest = digest("something-else")
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr); !errors.Is(err, auth.ErrDelegationInvalid) {
		t.Fatalf("bound-digest mismatch err = %v, want ErrDelegationInvalid", err)
	}
	// The exact bound digest verifies.
	pr2 := freshPresented("nonce-bd-ok", "messages")
	pr2.ContentDigest = digest("bound-to-this")
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr2); err != nil {
		t.Fatalf("matching bound digest: %v", err)
	}
}

func TestVerifyForgedSecretIndistinguishableFromUnknownSelector(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA)

	// Forge the secret: keep the real selector, swap in another valid-shape secret.
	other, err := auth.NewCredential(auth.PrefixDelegation)
	if err != nil {
		t.Fatal(err)
	}
	_, _, forgedSecret, _ := auth.ParseToken(other.Token)
	prefix, selector, _, _ := auth.ParseToken(token)
	forged := prefix + "_" + selector + "_" + forgedSecret

	_, forgedErr := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(forged), freshPresented("n-forge", "messages"))
	_, unknownErr := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(other.Token), freshPresented("n-unknown", "messages"))

	if !errors.Is(forgedErr, auth.ErrDelegationInvalid) || !errors.Is(unknownErr, auth.ErrDelegationInvalid) {
		t.Fatalf("forged=%v unknown=%v, want both ErrDelegationInvalid", forgedErr, unknownErr)
	}
	if forgedErr.Error() != unknownErr.Error() {
		t.Fatalf("forged and unknown errors are distinguishable: %q vs %q", forgedErr, unknownErr)
	}
}

func TestVerifyExpiredAndRevokedHandleDenied(t *testing.T) {
	f := newDelegFixture(t)

	// Revoked handle.
	revToken, revStored := f.mint(t, f.serviceA)
	if err := f.a.RevokeDelegationHandle(f.ctx, f.admin, revStored.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(revToken), freshPresented("n-rev", "messages")); !errors.Is(err, auth.ErrDelegationInvalid) {
		t.Errorf("revoked handle err = %v, want ErrDelegationInvalid", err)
	}

	// Expired handle: mint with a sub-microsecond TTL so it is already past its
	// ExpiresAt by the time the verify transaction reads the clock. A handle is
	// immutable after mint (no Update), so expiry is produced by time, not by
	// rewriting the row.
	expToken, _, err := f.a.MintDelegationHandle(f.ctx, f.admin, auth.MintDelegationRequest{
		SubjectToken: f.subjectSession,
		PEPServiceID: f.serviceA.ID,
		Operations:   []string{"messages"},
		TTL:          time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("mint short-lived handle: %v", err)
	}
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(expToken), freshPresented("n-exp", "messages")); !errors.Is(err, auth.ErrDelegationInvalid) {
		t.Errorf("expired handle err = %v, want ErrDelegationInvalid", err)
	}
}

func TestVerifyFreshnessWindow(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA)

	stale := freshPresented("n-stale", "messages")
	stale.IssuedAt = time.Now().Add(-10 * time.Minute)
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), stale); !errors.Is(err, auth.ErrDelegationReplay) {
		t.Errorf("stale IssuedAt err = %v, want ErrDelegationReplay", err)
	}

	token2, _ := f.mint(t, f.serviceA)
	future := freshPresented("n-future", "messages")
	future.IssuedAt = time.Now().Add(2 * time.Minute)
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token2), future); !errors.Is(err, auth.ErrDelegationReplay) {
		t.Errorf("future IssuedAt err = %v, want ErrDelegationReplay", err)
	}
}

// --- Lifecycle: effective = current ∩ mint ceiling ---------------------------

func TestVerifyRolePromotionDoesNotElevate(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA) // MintRole = editor

	// Promote the subject to admin AFTER mint.
	if _, err := f.a.GrantMembership(f.ctx, f.super, f.subjectUserID, f.tenant, auth.RoleAdmin, model.ID("")); err != nil {
		t.Fatal(err)
	}
	v, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("n-promote", "messages"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if v.EffectiveRole() != auth.RoleEditor {
		t.Errorf("effective role after promotion = %q, want editor (ceiling holds)", v.EffectiveRole())
	}
}

func TestVerifyRoleReductionLowersEffective(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA) // MintRole = editor
	if _, err := f.a.GrantMembership(f.ctx, f.super, f.subjectUserID, f.tenant, auth.RoleViewer, model.ID("")); err != nil {
		t.Fatal(err)
	}
	v, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("n-reduce", "messages"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if v.EffectiveRole() != auth.RoleViewer {
		t.Errorf("effective role = %q, want viewer (lower of current, mint)", v.EffectiveRole())
	}
}

func TestVerifyGroupAddedAfterMintDoesNotAppear(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA) // MintGroups = [groupID]

	// Add the subject to a NEW group after mint.
	newGroup := createGroupWithMember(t, f.ctx, f.st, f.tenant, f.subjectUserID)
	v, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("n-newgrp", "messages"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	for _, g := range v.EffectiveGroups() {
		if g == newGroup.String() {
			t.Fatalf("group added after mint appeared in effective groups: %v", v.EffectiveGroups())
		}
	}
	if len(v.EffectiveGroups()) != 1 || v.EffectiveGroups()[0] != f.groupID.String() {
		t.Errorf("effective groups = %v, want [%s]", v.EffectiveGroups(), f.groupID)
	}
}

func TestVerifyGroupRemovedAfterMintGone(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA)
	// Remove the subject from the mint-time group.
	if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
		rows, _, err := as.GroupMembers().List(f.ctx, model.Query{Filters: []model.Filter{
			{Column: "user_id", Op: model.OpEq, Value: f.subjectUserID.String()},
		}})
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := as.GroupMembers().Delete(f.ctx, r.ID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	v, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("n-rmgrp", "messages"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(v.EffectiveGroups()) != 0 {
		t.Errorf("effective groups after removal = %v, want empty", v.EffectiveGroups())
	}
}

func TestVerifyLifecycleInvalidations(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*testing.T, delegFixture)
	}{
		{
			name: "session revoked",
			corrupt: func(t *testing.T, f delegFixture) {
				if err := f.a.RevokeSession(f.ctx, f.super, f.subjectSessionID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "user disabled",
			corrupt: func(t *testing.T, f delegFixture) {
				if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
					u, err := as.Users().Get(f.ctx, f.subjectUserID)
					if err != nil {
						return err
					}
					u.Status = model.StatusInactive
					_, err = as.Users().Update(f.ctx, u)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "membership removed",
			corrupt: func(t *testing.T, f delegFixture) {
				if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
					ms, _, err := as.Memberships().List(f.ctx, model.Query{Filters: []model.Filter{
						{Column: "user_id", Op: model.OpEq, Value: f.subjectUserID.String()},
						{Column: "target_tenant_id", Op: model.OpEq, Value: f.tenant.String()},
					}})
					if err != nil {
						return err
					}
					for _, m := range ms {
						if err := as.Memberships().Delete(f.ctx, m.ID); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "confinement introduced",
			corrupt: func(t *testing.T, f delegFixture) {
				if _, err := f.a.GrantMembership(f.ctx, f.super, f.subjectUserID, f.tenant, auth.RoleEditor, model.NewID()); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDelegFixture(t)
			token, _ := f.mint(t, f.serviceA)
			tt.corrupt(t, f)
			if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("n-lc", "messages")); !errors.Is(err, auth.ErrDelegationInvalid) {
				t.Fatalf("%s verify err = %v, want ErrDelegationInvalid", tt.name, err)
			}
		})
	}
}

// --- Claim / replay ----------------------------------------------------------

func TestVerifyIdempotentRetrySameKey(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA)
	pr := freshPresented("nonce-retry", "messages")

	first, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr)
	if err != nil {
		t.Fatalf("first verify: %v", err)
	}
	second, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr)
	if err != nil {
		t.Fatalf("retry verify: %v", err)
	}
	if !second.Retried() {
		t.Error("second verify with identical request must be a retry")
	}
	if first.DecisionID() != second.DecisionID() {
		t.Errorf("retry decision id = %q, want %q", second.DecisionID(), first.DecisionID())
	}
}

func TestVerifyReplayVariants(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA)
	base := freshPresented("nonce-shared", "messages")
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), base); err != nil {
		t.Fatalf("initial claim: %v", err)
	}

	// same nonce, DIFFERENT fingerprint (different content digest) → replay.
	diffFp := base
	diffFp.ContentDigest = digest("different")
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), diffFp); !errors.Is(err, auth.ErrDelegationReplay) {
		t.Errorf("same nonce/different fingerprint err = %v, want ErrDelegationReplay", err)
	}

	// same handle, DIFFERENT nonce → replay (single-use handle).
	diffNonce := freshPresented("nonce-other", "messages")
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), diffNonce); !errors.Is(err, auth.ErrDelegationReplay) {
		t.Errorf("same handle/different nonce err = %v, want ErrDelegationReplay", err)
	}

	// same nonce, DIFFERENT handle → replay (nonce reused across handles for one service).
	token2, _ := f.mint(t, f.serviceA)
	sameNonceOtherHandle := freshPresented("nonce-shared", "messages")
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token2), sameNonceOtherHandle); !errors.Is(err, auth.ErrDelegationReplay) {
		t.Errorf("same nonce/different handle err = %v, want ErrDelegationReplay", err)
	}
}

func TestVerifySameNonceDifferentServiceIndependent(t *testing.T) {
	f := newDelegFixture(t)
	tokenA, _ := f.mint(t, f.serviceA)
	tokenB, _ := f.mint(t, f.serviceB)
	nonce := "nonce-per-service"

	a, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(tokenA), freshPresented(nonce, "messages"))
	if err != nil {
		t.Fatalf("service A claim: %v", err)
	}
	b, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepB, handleProof(tokenB), freshPresented(nonce, "messages"))
	if err != nil {
		t.Fatalf("service B claim with same nonce: %v", err)
	}
	if a.DecisionID() == b.DecisionID() {
		t.Error("same nonce for two services must yield independent decisions")
	}
}

func TestVerifyConcurrentSameKeyExactlyOneCreated(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA)
	pr := freshPresented("nonce-concurrent", "messages")

	type result struct {
		v   auth.VerifiedDelegation
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			v, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr)
			results <- result{v, err}
		}()
	}
	ready.Wait()
	close(start)
	r1 := <-results
	r2 := <-results
	if r1.err != nil || r2.err != nil {
		t.Fatalf("concurrent errs: %v / %v", r1.err, r2.err)
	}
	created := 0
	if !r1.v.Retried() {
		created++
	}
	if !r2.v.Retried() {
		created++
	}
	if created != 1 {
		t.Fatalf("created (non-retried) count = %d, want 1", created)
	}
	if r1.v.DecisionID() != r2.v.DecisionID() {
		t.Fatalf("concurrent decisions differ: %q != %q", r1.v.DecisionID(), r2.v.DecisionID())
	}
}

func TestCrashShapeRetryResumesPendingHandleNotBurned(t *testing.T) {
	f := newDelegFixture(t)
	token, stored := f.mint(t, f.serviceA)
	pr := freshPresented("nonce-crash", "messages")

	first, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	// No finalize (simulated crash). Retry the exact same request.
	resumed, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !resumed.Retried() || resumed.DecisionID() != first.DecisionID() {
		t.Errorf("resume = {retried %v, id %q}, want {true, %q}", resumed.Retried(), resumed.DecisionID(), first.DecisionID())
	}
	if resumed.StoredVerdictJSON() != "" {
		t.Errorf("pending resume verdict = %q, want empty", resumed.StoredVerdictJSON())
	}
	// The handle row itself was never burned (crash-safe).
	h, gerr := f.getHandle(t, stored.ID)
	if gerr != nil {
		t.Fatalf("handle must still exist: %v", gerr)
	}
	if h.RevokedAt != nil {
		t.Error("handle must NOT be revoked/burned at claim time")
	}

	// After finalize, a retry surfaces the stored verdict.
	verdict := []byte(`{"decision":"allow"}`)
	if err := f.a.FinalizeDecisionClaim(f.ctx, first.DecisionID(), verdict, "policy-v1"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	final, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr)
	if err != nil {
		t.Fatalf("post-finalize retry: %v", err)
	}
	if final.StoredVerdictJSON() != string(verdict) {
		t.Errorf("post-finalize verdict = %q, want %q", final.StoredVerdictJSON(), verdict)
	}
}

// --- Finalize ----------------------------------------------------------------

func TestFinalizeIdempotentAndContradictionRefused(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA)
	v, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("n-fin", "messages"))
	if err != nil {
		t.Fatal(err)
	}
	verdict := []byte(`{"decision":"deny"}`)
	if err := f.a.FinalizeDecisionClaim(f.ctx, v.DecisionID(), verdict, "p1"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	// Idempotent on identical content.
	if err := f.a.FinalizeDecisionClaim(f.ctx, v.DecisionID(), verdict, "p1"); err != nil {
		t.Fatalf("idempotent finalize: %v", err)
	}
	// Contradictory finalize refused.
	if err := f.a.FinalizeDecisionClaim(f.ctx, v.DecisionID(), []byte(`{"decision":"allow"}`), "p1"); !errors.Is(err, auth.ErrDecisionFinalizeConflict) {
		t.Errorf("contradictory finalize err = %v, want ErrDecisionFinalizeConflict", err)
	}
	// Unknown decision.
	if err := f.a.FinalizeDecisionClaim(f.ctx, model.NewID(), verdict, "p1"); !errors.Is(err, auth.ErrUnknownDecision) {
		t.Errorf("unknown finalize err = %v, want ErrUnknownDecision", err)
	}
}

// --- Phase binding -----------------------------------------------------------

func TestCheckDecisionServiceBinding(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA)
	nonce := "nonce-bind"
	v, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented(nonce, "messages"))
	if err != nil {
		t.Fatal(err)
	}

	if err := f.a.CheckDecisionServiceBinding(f.ctx, v.DecisionID(), nonce, f.pepA); err != nil {
		t.Fatalf("valid binding: %v", err)
	}
	if err := f.a.CheckDecisionServiceBinding(f.ctx, v.DecisionID(), nonce, f.pepB); !errors.Is(err, auth.ErrDecisionBindingMismatch) {
		t.Errorf("wrong service err = %v, want ErrDecisionBindingMismatch", err)
	}
	if err := f.a.CheckDecisionServiceBinding(f.ctx, v.DecisionID(), "wrong-nonce", f.pepA); !errors.Is(err, auth.ErrDecisionBindingMismatch) {
		t.Errorf("wrong nonce err = %v, want ErrDecisionBindingMismatch", err)
	}
	if err := f.a.CheckDecisionServiceBinding(f.ctx, model.NewID(), nonce, f.pepA); !errors.Is(err, auth.ErrDecisionBindingMismatch) {
		t.Errorf("unknown decision err = %v, want ErrDecisionBindingMismatch", err)
	}

	// Expired decision: the claim surface is read-only (its ClaimedAt cannot be
	// mutated), so shrink the configured lifetime instead — the real time elapsed
	// since ClaimedAt now exceeds it, tripping the freshness bound.
	f.a.SetDecisionClaimLifetime(time.Nanosecond)
	if err := f.a.CheckDecisionServiceBinding(f.ctx, v.DecisionID(), nonce, f.pepA); !errors.Is(err, auth.ErrDecisionBindingMismatch) {
		t.Errorf("expired decision err = %v, want ErrDecisionBindingMismatch", err)
	}
}

// --- Capabilities ------------------------------------------------------------

func TestEffectiveCapabilitiesTruthTable(t *testing.T) {
	declared := map[string]bool{"buffer_request": true, "streaming": true, "batch": true, "off": false}
	registered := map[string]bool{"buffer_request": true, "streaming": false}
	eff, dropped := auth.EffectiveCapabilities(declared, registered)

	if len(eff) != 1 || !eff["buffer_request"] {
		t.Errorf("effective = %v, want {buffer_request:true}", eff)
	}
	// streaming declared-but-registered-false, batch declared-but-unregistered → both dropped.
	if !dropped["streaming"] || !dropped["batch"] {
		t.Errorf("dropped = %v, want streaming+batch", dropped)
	}
	if dropped["off"] || eff["off"] {
		t.Error("a non-declared (false) capability is neither effective nor a dropped overclaim")
	}
}

// --- GC ----------------------------------------------------------------------

func TestSweepDeletesExpiredUnclaimedButRetainsClaimed(t *testing.T) {
	f := newDelegFixture(t)

	// An expired, unclaimed handle (past grace) → swept.
	_, unclaimed := f.mint(t, f.serviceA)
	// A claimed handle (also expired past grace) → retained, its claim survives.
	claimedTok, claimed := f.mint(t, f.serviceA)
	v, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(claimedTok), freshPresented("n-gc", "messages"))
	if err != nil {
		t.Fatal(err)
	}

	// Both handles are minted with the default (5-minute) TTL; a handle is immutable
	// after mint, so instead of rewriting ExpiresAt we sweep at a time well past
	// their expiry-plus-grace. The sweep cutoff derives from the passed `now`, not
	// the authenticator clock.
	counts, err := f.a.SweepDelegation(f.ctx, model.NewTimestamp(time.Now().Add(200*time.Hour)), 100)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if counts.ExpiredHandlesDeleted != 1 {
		t.Errorf("deleted = %d, want 1 (only the unclaimed handle)", counts.ExpiredHandlesDeleted)
	}
	if _, gerr := f.getHandle(t, unclaimed.ID); !errors.Is(gerr, store.ErrNotFound) {
		t.Errorf("unclaimed expired handle should be swept, got %v", gerr)
	}
	if _, gerr := f.getHandle(t, claimed.ID); gerr != nil {
		t.Errorf("claimed handle must be retained, got %v", gerr)
	}
	// The claim (pending) survives regardless.
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		_, err := as.PDPDecisionClaims().Get(f.ctx, v.DecisionID())
		return err
	}); err != nil {
		t.Errorf("pending claim must never be reaped: %v", err)
	}
}

func TestSweepRespectsGraceAndLimit(t *testing.T) {
	f := newDelegFixture(t)

	// A handle inside the grace period → not swept. It is minted with the default
	// 5-minute TTL, so sweeping at now+10m makes it expired (past ExpiresAt) but well
	// within the 1h grace window; the cutoff (sweep now − grace) is before its
	// expiry, so it is not selected. Handles are immutable after mint, so the age is
	// produced by the sweep clock, not by rewriting the row.
	f.mint(t, f.serviceA)
	counts, err := f.a.SweepDelegation(f.ctx, model.NewTimestamp(time.Now().Add(10*time.Minute)), 100)
	if err != nil {
		t.Fatal(err)
	}
	if counts.ExpiredHandlesDeleted != 0 {
		t.Errorf("recently-expired handle within grace swept = %d, want 0", counts.ExpiredHandlesDeleted)
	}

	// Bounded batch: three more unclaimed handles (default TTL), swept well past
	// expiry-plus-grace with limit 2 → at most 2 removed in the batch.
	for range 3 {
		f.mint(t, f.serviceA)
	}
	counts, err = f.a.SweepDelegation(f.ctx, model.NewTimestamp(time.Now().Add(200*time.Hour)), 2)
	if err != nil {
		t.Fatal(err)
	}
	if counts.ExpiredHandlesDeleted > 2 {
		t.Errorf("bounded batch deleted = %d, want <= 2", counts.ExpiredHandlesDeleted)
	}
}

// --- Hygiene -----------------------------------------------------------------

func TestNoSecretNonceOrContentInErrorsOrAudit(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA)

	const secretNonce = "SUPER-SECRET-NONCE-VALUE"
	secretContent := digest("SECRET-CONTENT-DIGEST")
	_, prefix, secret, _ := parts(token)

	// A successful claim (writes an audit event) then a replay (returns an error).
	base := freshPresented(secretNonce, "messages")
	base.ContentDigest = secretContent
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), base); err != nil {
		t.Fatalf("claim: %v", err)
	}
	replay := base
	replay.ContentDigest = digest("another")
	_, replayErr := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), replay)
	if replayErr == nil {
		t.Fatal("expected a replay error")
	}
	for _, needle := range []string{secretNonce, secretContent, secret, prefix} {
		if needle != "" && strings.Contains(replayErr.Error(), needle) {
			t.Errorf("error message leaks %q: %q", needle, replayErr)
		}
	}

	// Audit meta/action must not carry the raw nonce/secret/content either.
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		return as.Audit().Walk(f.ctx, 1, func(ev model.AuditEvent) error {
			blob := ev.Action + " " + fmtMeta(ev.Meta)
			for _, needle := range []string{secretNonce, secretContent, secret} {
				if strings.Contains(blob, needle) {
					t.Errorf("audit event %q leaks a secret: %s", ev.Action, blob)
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk audit: %v", err)
	}
}

// --- H2: cached PEP identity revalidation ------------------------------------

// TestVerifyRevalidatesPEPCredential pins H2: a PEPIdentity is cached by the
// caller, so VerifyAndClaimDelegation must re-check the PEP transport credential
// inside the claim transaction. A credential revoked/expired/unbound AFTER
// authentication can no longer claim a decision.
func TestVerifyRevalidatesPEPCredential(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*testing.T, delegFixture)
	}{
		{
			name: "credential token revoked",
			corrupt: func(t *testing.T, f delegFixture) {
				if err := f.a.RevokeToken(f.ctx, f.admin, f.pepA.CredentialID()); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "credential token expired",
			corrupt: func(t *testing.T, f delegFixture) {
				expired := model.NewTimestamp(time.Now().Add(-time.Minute))
				if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
					tok, err := as.Tokens().Get(f.ctx, f.pepA.CredentialID())
					if err != nil {
						return err
					}
					tok.ExpiresAt = &expired
					_, err = as.Tokens().Update(f.ctx, tok)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "credential mapping unbound",
			corrupt: func(t *testing.T, f delegFixture) {
				if err := f.a.UnbindPEPCredential(f.ctx, f.admin, f.serviceA.ID, f.pepA.CredentialID()); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDelegFixture(t)
			token, _ := f.mint(t, f.serviceA)
			// f.pepA was authenticated when the fixture was built; corrupt its
			// credential now (the cached-identity reuse window).
			tt.corrupt(t, f)
			if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("n-h2-"+tt.name, "messages")); !errors.Is(err, auth.ErrDelegationInvalid) {
				t.Fatalf("%s verify err = %v, want ErrDelegationInvalid", tt.name, err)
			}
		})
	}
}

// --- H3 + M5: capability vocabulary, injective fingerprint, overclaim audit ---

// TestVerifyUnknownCapabilityDroppedAndFingerprintStable pins H3: a declared key
// outside the SDK vocabulary is dropped from the effective set, never audited, and
// (being excluded from the fingerprint) does not turn an otherwise identical retry
// into a replay.
func TestVerifyUnknownCapabilityDroppedAndFingerprintStable(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA)

	pr := freshPresented("nonce-h3", "messages")
	pr.DeclaredCapabilities = map[string]bool{"buffer_request": true, "totally_unknown": true}
	v1, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if v1.EffectiveCapabilities()["totally_unknown"] {
		t.Error("unknown capability leaked into the effective set")
	}
	if v1.DroppedCapabilities()["totally_unknown"] {
		t.Error("unknown capability must be dropped SILENTLY, never recorded as an overclaim")
	}

	// A retry omitting the unknown key is idempotent — the unknown key never entered
	// the fingerprint, so the two requests are the same claim. Reuse the SAME base
	// (identical IssuedAt) and change only the declared capabilities.
	retry := pr
	retry.DeclaredCapabilities = map[string]bool{"buffer_request": true}
	v2, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), retry)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !v2.Retried() || v2.DecisionID() != v1.DecisionID() {
		t.Errorf("retry = {retried %v, id %q}, want idempotent {true, %q}", v2.Retried(), v2.DecisionID(), v1.DecisionID())
	}
}

// TestVerifyAuditsCapabilityOverclaim pins M5: a KNOWN vocabulary capability that
// is declared but not registered is dropped AND audited (sanitized), while an
// unknown key never reaches the audit trail.
func TestVerifyAuditsCapabilityOverclaim(t *testing.T) {
	f := newDelegFixture(t) // serviceA registers buffer_request + streaming
	token, _ := f.mint(t, f.serviceA)

	pr := freshPresented("nonce-m5", "messages")
	pr.DeclaredCapabilities = map[string]bool{"buffer_request": true, "batch": true, "totally_unknown": true}
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// The audit read path discards the in-memory Meta map, so read the STORED
	// canonical meta string through the canonical walker.
	var overclaim string
	found := false
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		cw, ok := as.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("audit log does not expose the canonical walker")
		}
		return cw.WalkCanonical(f.ctx, 1, func(ev model.AuditEvent, meta string, _ []byte) error {
			if ev.Action == "delegation.capability_overclaim" {
				found = true
				overclaim = meta
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected a delegation.capability_overclaim audit event")
	}
	if !strings.Contains(overclaim, "batch") {
		t.Errorf("overclaim audit meta = %q, want it to name the dropped known cap batch", overclaim)
	}
	if strings.Contains(overclaim, "totally_unknown") {
		t.Errorf("overclaim audit meta = %q, must NOT name an unknown attacker-controlled key", overclaim)
	}
}

// --- RETRY-COHERENCE: caps read at claim time; retry surfaces stored -----------

// TestVerifyRecordsCapsAtClaimTimeAndRetryReplaysStored pins the retry-coherence
// fix: effective capabilities are intersected against the service registration
// read INSIDE the transaction (not the stale PEPIdentity snapshot), and an
// idempotent retry surfaces the STORED caps/version, never a fresh read.
func TestVerifyRecordsCapsAtClaimTimeAndRetryReplaysStored(t *testing.T) {
	f := newDelegFixture(t) // serviceA v1 registers buffer_request + streaming; f.pepA snapshot is v1
	token, _ := f.mint(t, f.serviceA)

	// Register a NEW capability and bump the version AFTER AuthenticatePEP.
	if err := f.a.UpdatePEPServiceCapabilities(f.ctx, f.admin, f.serviceA.ID, map[string]bool{
		"buffer_request": true, "streaming": true, "buffer_response": true,
	}); err != nil {
		t.Fatal(err)
	}

	pr := freshPresented("nonce-coherence", "messages")
	pr.DeclaredCapabilities = map[string]bool{"buffer_request": true, "buffer_response": true}
	first, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	// The claim records the caps/version read at claim time (v2), NOT the stale pep
	// snapshot (v1, which did not register buffer_response).
	if first.CapabilityVersion() != 2 {
		t.Errorf("claim capability version = %d, want 2 (read at claim time)", first.CapabilityVersion())
	}
	if !first.EffectiveCapabilities()["buffer_response"] {
		t.Errorf("effective caps = %v, want buffer_response (registered at claim time)", first.EffectiveCapabilities())
	}

	second, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !second.Retried() {
		t.Fatal("second identical claim must be a retry")
	}
	if second.CapabilityVersion() != first.CapabilityVersion() {
		t.Errorf("retry version = %d, want stored %d", second.CapabilityVersion(), first.CapabilityVersion())
	}
	if !second.EffectiveCapabilities()["buffer_response"] {
		t.Errorf("retry effective caps = %v, want the stored set (with buffer_response)", second.EffectiveCapabilities())
	}
}

// --- H4: finalize idempotency/atomicity extras -------------------------------

// TestFinalizeRejectsInvalidVerdictAndPolicyConflict pins H4 parts (1) and (3):
// an empty/non-JSON verdict is refused, and idempotency requires BOTH the verdict
// hash AND the policy version to match (same hash, different policy = conflict).
func TestFinalizeRejectsInvalidVerdictAndPolicyConflict(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA)
	v, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("n-h4", "messages"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.FinalizeDecisionClaim(f.ctx, v.DecisionID(), []byte(""), "p1"); !errors.Is(err, auth.ErrInvalidVerdict) {
		t.Errorf("empty verdict err = %v, want ErrInvalidVerdict", err)
	}
	if err := f.a.FinalizeDecisionClaim(f.ctx, v.DecisionID(), []byte("not json"), "p1"); !errors.Is(err, auth.ErrInvalidVerdict) {
		t.Errorf("non-JSON verdict err = %v, want ErrInvalidVerdict", err)
	}
	verdict := []byte(`{"decision":"allow"}`)
	if err := f.a.FinalizeDecisionClaim(f.ctx, v.DecisionID(), verdict, "policy-1"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := f.a.FinalizeDecisionClaim(f.ctx, v.DecisionID(), verdict, "policy-1"); err != nil {
		t.Fatalf("idempotent (same hash + same policy) finalize: %v", err)
	}
	if err := f.a.FinalizeDecisionClaim(f.ctx, v.DecisionID(), verdict, "policy-2"); !errors.Is(err, auth.ErrDecisionFinalizeConflict) {
		t.Errorf("same hash + different policy err = %v, want ErrDecisionFinalizeConflict", err)
	}
}

// TestFinalizeRefusesExpiredClaim pins H4 part (2): a claim past its single-use
// lifetime is never finalized.
func TestFinalizeRefusesExpiredClaim(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA)
	v, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("n-h4-exp", "messages"))
	if err != nil {
		t.Fatal(err)
	}
	// Shrink the lifetime so the real time elapsed since ClaimedAt exceeds it.
	f.a.SetDecisionClaimLifetime(time.Nanosecond)
	if err := f.a.FinalizeDecisionClaim(f.ctx, v.DecisionID(), []byte(`{"decision":"allow"}`), "p1"); !errors.Is(err, auth.ErrDecisionExpired) {
		t.Errorf("expired-claim finalize err = %v, want ErrDecisionExpired", err)
	}
}

// TestFinalizeConcurrentIdenticalBothSucceed pins H4 part (4): two identical
// finalizes must both return nil — the version-locked transition that loses the
// race is reloaded and re-classified, never leaking store.ErrConflict.
func TestFinalizeConcurrentIdenticalBothSucceed(t *testing.T) {
	f := newDelegFixture(t)
	token, _ := f.mint(t, f.serviceA)
	v, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("n-h4-conc", "messages"))
	if err != nil {
		t.Fatal(err)
	}
	verdict := []byte(`{"decision":"deny"}`)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			errs <- f.a.FinalizeDecisionClaim(f.ctx, v.DecisionID(), verdict, "p1")
		}()
	}
	ready.Wait()
	close(start)
	if e1, e2 := <-errs, <-errs; e1 != nil || e2 != nil {
		t.Fatalf("concurrent identical finalize errs = %v / %v, want both nil", e1, e2)
	}
}

// --- M2: TTL is a maximum, only shortened by an explicit request --------------

// TestMintDelegationCapsTTLAtConfiguredMax pins M2: a request cannot EXTEND the
// handle past the configured TTL. A no-expiry token subject (no expiry clamp)
// asking for an enormous TTL still expires at now + the configured max.
func TestMintDelegationCapsTTLAtConfiguredMax(t *testing.T) {
	f := newDelegFixture(t)
	subTok, _, err := f.a.IssueToken(f.ctx, f.super, auth.TokenSpec{
		Name: "no-expiry-subject", BoundTenant: f.tenant, Role: auth.RoleEditor,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	_, stored, err := f.a.MintDelegationHandle(f.ctx, f.admin, auth.MintDelegationRequest{
		SubjectToken: subTok, PEPServiceID: f.serviceA.ID,
		Operations: []string{"messages"}, TTL: 100000 * time.Hour,
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	maxExpiry := before.Add(auth.DefaultDelegationTTL + time.Minute)
	if stored.ExpiresAt.Time().After(maxExpiry) {
		t.Errorf("handle expiry = %s, want clamped to now + configured max (~%s), not the requested 100000h",
			stored.ExpiresAt, auth.DefaultDelegationTTL)
	}
}

func parts(token string) (kind, prefix, secret string, ok bool) {
	p, sel, sec, k := auth.ParseToken(token)
	return "", p, sec, k && sel != ""
}

func fmtMeta(m map[string]any) string {
	var b strings.Builder
	for k, v := range m {
		b.WriteString(k)
		b.WriteByte('=')
		if s, isStr := v.(string); isStr {
			b.WriteString(s)
		}
		b.WriteByte(' ')
	}
	return b.String()
}

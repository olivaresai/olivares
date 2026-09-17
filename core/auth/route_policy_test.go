// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestResourceDigestIsLengthPrefixed is the collision test, and it is the reason the
// digest is not a join.
//
// Two DIFFERENT resources that produce ONE digest means a witness for the first
// authorizes the second. With a separator-joined digest, `Kind:"a", ID:"b:c"` and
// `Kind:"a:b", ID:"c"` collide — and an entity id can contain a colon.
func TestResourceDigestIsLengthPrefixed(t *testing.T) {
	a := ResourceDigest("t1", ResourceAttrs{Kind: "a", ID: "b:c"})
	b := ResourceDigest("t1", ResourceAttrs{Kind: "a:b", ID: "c"})
	if a == b {
		t.Fatal("two different resources share one digest: a witness for either authorizes " +
			"the other")
	}
	// The tenant is inside the digest too: the same row in two tenants is two resources.
	if ResourceDigest("t1", ResourceAttrs{Kind: "session", ID: "x"}) ==
		ResourceDigest("t2", ResourceAttrs{Kind: "session", ID: "x"}) {
		t.Fatal("the same id in two tenants shares one digest")
	}
	// And the control positive: the SAME resource digests the same, or nothing that
	// compares digests would ever match.
	if ResourceDigest("t1", ResourceAttrs{Kind: "session", ID: "x"}) !=
		ResourceDigest("t1", ResourceAttrs{Kind: "session", ID: "x"}) {
		t.Fatal("the digest is not stable for one resource")
	}
}

// TestResourceDigestCoversExtraDeterministically: Extra participates, and map iteration
// order must not change the answer.
func TestResourceDigestCoversExtraDeterministically(t *testing.T) {
	r1 := ResourceAttrs{Kind: "session", ID: "x", Extra: map[string]string{"a": "1", "b": "2"}}
	r2 := ResourceAttrs{Kind: "session", ID: "x", Extra: map[string]string{"b": "2", "a": "1"}}
	if ResourceDigest("t", r1) != ResourceDigest("t", r2) {
		t.Fatal("the digest depends on map iteration order: the same resource would digest " +
			"differently between two calls in one process")
	}
	r3 := ResourceAttrs{Kind: "session", ID: "x", Extra: map[string]string{"a": "1", "b": "3"}}
	if ResourceDigest("t", r1) == ResourceDigest("t", r3) {
		t.Fatal("a changed Extra value does not change the digest")
	}
}

// TestWitnessWithNoWindowIsNotFresh: a zero FreshUntil is an evidence window that was
// never established, and unknown denies. Reading it as "no expiry" would make every
// unestablished decision permanent.
func TestWitnessWithNoWindowIsNotFresh(t *testing.T) {
	now := time.Now()
	var w RouteAuthorizationWitness
	if w.IsFresh(now) {
		t.Fatal("a witness with no evidence window reads as fresh: an unestablished decision " +
			"would never expire")
	}
	w.Decision.Outcome = EvidenceAllow
	if w.Allows(now) {
		t.Fatal("an ALLOW with no window authorizes")
	}
	// Los DOS extremos: IsFresh dejó de aceptar una ventana a medias, así que dar sólo el
	// superior deja al testigo sin ventana y no dentro de ella. Un intervalo con un solo extremo
	// no es un intervalo, y antes se leía como uno abierto por abajo — que autoriza una decisión
	// que todavía no se había tomado.
	w.Decision.ObservedAt = now.Add(-time.Second)
	w.Decision.FreshUntil = now.Add(time.Minute)

	// ⛔ AND STILL NOT, BECAUSE NOTHING MINTED IT. Outcome and window are the two facts a
	// forger can type; the seal is the one it cannot, because it is unexported and only
	// AuthorizeRoute sets it. This line is the whole of R-630 in one assertion: before it,
	// "a witness cannot be fabricated outside the authorization" was a sentence in a doc
	// comment, and the only value that actually denied was the zero one.
	if w.Allows(now) {
		t.Fatal("a witness nobody minted authorizes on outcome and window alone: a value typed " +
			"in any package would be honoured as an authorization")
	}
	w.minted = true
	if !w.Allows(now) {
		t.Fatal("an ALLOW inside its window does not authorize")
	}
	if w.Allows(now.Add(2 * time.Minute)) {
		t.Fatal("an ALLOW authorizes after its window closed")
	}
	// And an allow-shaped window over a DENY is still a deny.
	w.Decision.Outcome = EvidenceDeny
	if w.Allows(now) {
		t.Fatal("a DENY with a fresh window authorizes")
	}
}

// TestDenialForKeepsTheThreeAnswersApart is canon rule 5 at the seam: deny, undecided and
// "there was nobody to ask" are three different sentences with three different remedies.
func TestDenialForKeepsTheThreeAnswersApart(t *testing.T) {
	if err := DenialFor(AuthorizationEvidence{Outcome: EvidenceDeny}); !errors.Is(err, ErrRouteDenied) {
		t.Errorf("a DENY mapped to %v", err)
	}
	if err := DenialFor(AuthorizationEvidence{Outcome: EvidenceUnknown}); !errors.Is(err, ErrRouteUndecided) {
		t.Errorf("an UNKNOWN mapped to %v", err)
	}
	// A denial caused by a missing scoped grant is distinguishable, so the client can be
	// told which remedy applies instead of a flat "no".
	scoped := DenialFor(AuthorizationEvidence{
		Outcome:        EvidenceDeny,
		CorePermission: CheckEvidence{Verdict: CheckBroken, Code: "scoped_grant_unavailable"},
	})
	if !errors.Is(scoped, ErrScopedGrantRequired) {
		t.Errorf("a scoped-grant denial mapped to %v", scoped)
	}
}

// TestAuthorizeRouteChecksAssuranceBeforeEvaluating pins the ORDER, not just the outcome.
//
// A principal who must step up is not told, by which error comes back, whether they would
// have been authorized afterwards. Evaluating first and reporting the step-up second would
// leak exactly that.
func TestAuthorizeRouteChecksAssuranceBeforeEvaluating(t *testing.T) {
	az := NewAuthorizer(nil)
	_, err := az.AuthorizeRoute(t.Context(), Request{
		Principal:  Principal{Kind: KindUser, AAL: AAL1},
		Permission: "session-cockpit:stop:write",
		Tenant:     "t1",
		Resource:   ResourceAttrs{Kind: "session", ID: "s1"},
		Route:      RouteMetadata{MinimumAAL: AAL3, CedarAction: "session:stop", RequireScopedGrant: true},
	})
	if !errors.Is(err, ErrStepUpRequired) {
		t.Fatalf("an AAL1 principal on an AAL3 route got %v, want a step-up", err)
	}
	// The control in the other direction: with the assurance satisfied, the answer is a
	// policy answer and NOT a step-up. Without a scoped engine this principal has no
	// grant, so it is a denial — which is exactly the point: the two errors are different.
	_, err = az.AuthorizeRoute(t.Context(), Request{
		Principal:  Principal{Kind: KindUser, AAL: AAL3},
		Permission: "session-cockpit:stop:write",
		Tenant:     "t1",
		Resource:   ResourceAttrs{Kind: "session", ID: "s1"},
		Route:      RouteMetadata{MinimumAAL: AAL3, CedarAction: "session:stop", RequireScopedGrant: true},
	})
	if errors.Is(err, ErrStepUpRequired) {
		t.Fatal("an AAL3 principal on an AAL3 route was told to step up")
	}
	if err == nil {
		t.Fatal("a principal with no grant was authorized on a scoped-grant route")
	}
}

// TestAuthorizeRouteReturnsNoWitnessOnDenial: a witness for a denial is a record that
// reads like a permission at every call site that forgets to check the outcome.
func TestAuthorizeRouteReturnsNoWitnessOnDenial(t *testing.T) {
	az := NewAuthorizer(nil)
	w, err := az.AuthorizeRoute(t.Context(), Request{
		Principal:  Principal{Kind: KindUser, AAL: AAL3},
		Permission: "session-cockpit:stop:write",
		Tenant:     "t1",
		Resource:   ResourceAttrs{Kind: "session", ID: "s1"},
		Route:      RouteMetadata{CedarAction: "session:stop", RequireScopedGrant: true},
	})
	if err == nil {
		t.Fatal("the denial did not happen; this test cannot measure what it claims")
	}
	// The witness contains slices, so it is compared field by field rather than with ==.
	// Comparing only Allows() would pass for a witness that carried a real action and a
	// real resource digest and merely lacked a window.
	if w.CedarAction != "" || w.ScopedEffect != EffectAbstain || w.PolicyVersion != 0 ||
		w.ResourceDigest != ([32]byte{}) || w.EvidenceDigest != ([32]byte{}) ||
		w.minted || !w.Decision.ObservedAt.IsZero() || !w.Decision.FreshUntil.IsZero() ||
		w.Decision.Outcome != EvidenceUnknown {
		t.Fatalf("a denial produced a non-zero witness: %+v", w)
	}
	if w.Allows(time.Now()) {
		t.Fatal("the zero witness authorizes")
	}
}

// TestNilAuthorizerIsNotADenial is the third answer at the top of the seam.
func TestNilAuthorizerIsNotADenial(t *testing.T) {
	var az *Authorizer
	_, err := az.AuthorizeRoute(t.Context(), Request{})
	if !errors.Is(err, ErrAuthorizerUnavailable) {
		t.Fatalf("a nil authorizer answered %v, want the 'nothing was evaluated' error", err)
	}
	if errors.Is(err, ErrRouteDenied) {
		t.Fatal("'nobody evaluated this' was rendered as a policy denial: an operator would " +
			"read it as their policy refusing something")
	}
}

// TestCedarActionFallsBackToThePermission keeps every route that has not opted in
// deciding exactly as modules/governance already does.
func TestCedarActionFallsBackToThePermission(t *testing.T) {
	if got := resolveCedarAction(Request{Permission: "core:agent:read"}); got != "core:agent:read" {
		t.Errorf("with no declared action, got %q, want the permission", got)
	}
	if got := resolveCedarAction(Request{
		Permission: "session-cockpit:shell:write",
		Route:      RouteMetadata{CedarAction: "shell:open"},
	}); got != "shell:open" {
		t.Errorf("the declared action was not used: %q", got)
	}
}

// countingScoped answers GRANT the first time and FORBID afterwards, and counts.
//
// ⛔ IT IS THE VERIFICATION MUTANT FOR A11. AuthorizeRoute documented "ONE evaluation, not
// two" and then made two: AuthorizeEvidence asked this engine, and the next statement asked
// it again through scopedSafe. An engine whose answer changes between the calls is what
// production looks like when a grant is revoked, a policy is republished or a directory
// fact expires mid-request - and the witness would then attest an effect that had no part
// in the decision it accompanies.
type countingScoped struct {
	calls    int
	evidence []ScopedEvidenceDecision
}

func (c *countingScoped) Scoped(context.Context, Request) (ScopedDecision, error) {
	c.calls++
	return ScopedDecision{Effect: EffectForbid, Reason: "second look"}, nil
}

func (c *countingScoped) ScopedEvidence(context.Context, Request) (ScopedEvidenceDecision, error) {
	i := c.calls
	c.calls++
	if i >= len(c.evidence) {
		i = len(c.evidence) - 1
	}
	return c.evidence[i], nil
}

func TestAuthorizeRouteAsksTheEngineExactlyOnce(t *testing.T) {
	now := time.Now()
	grant := ScopedEvidenceDecision{
		Effect:        EffectGrant,
		ResourceGuard: CheckEvidence{Verdict: CheckClean, Code: "guard_clean"},
		ForbidAbsence: CheckEvidence{Verdict: CheckClean, Code: "no_forbid"},
		Facts: []store.AuthorizationFactRef{
			{Kind: "grant", ID: "g1", Version: 7},
		},
		ObservedAt: now, FreshUntil: now.Add(time.Minute),
	}
	forbid := grant
	forbid.Effect = EffectForbid
	engine := &countingScoped{evidence: []ScopedEvidenceDecision{grant, forbid}}

	az := NewAuthorizer(nil, WithScopedGrants(engine))
	w, err := az.AuthorizeRoute(t.Context(), Request{
		Principal:  Principal{Kind: KindUser, AAL: AAL3, UserID: "u1"},
		Permission: "session-cockpit:stop:write",
		Tenant:     "t1",
		Resource:   ResourceAttrs{Kind: "session", ID: "s1"},
		Route: RouteMetadata{
			MinimumAAL: AAL3, CedarAction: "session:stop", RequireScopedGrant: true,
		},
	})
	if engine.calls != 1 {
		t.Errorf("the engine was asked %d times; the function's own contract is ONE "+
			"evaluation, and a second one can disagree with the first", engine.calls)
	}
	if engine.calls > 1 && err == nil && w.ScopedEffect == EffectForbid {
		t.Error("the witness attests FORBID while the decision it accompanies allowed: the " +
			"two observations disagreed and the witness carries the later one")
	}
	if err == nil && w.ScopedEffect != EffectGrant {
		t.Errorf("the witness attests effect %v, want the GRANT that produced the allow",
			w.ScopedEffect)
	}
}

// TestTheEvidenceDigestChangesWhenTheEvidenceChanges is the other half of A11.
//
// EvidenceDigest is documented as binding the evidence, and it covered the action, three
// CODES, the resource digest and the highest policy version. Two witnesses over the same
// route and resource at the same epoch, with different outcomes, different verdicts, a
// different scoped effect, a different window and entirely different facts, produced the
// SAME digest. A digest that does not change when the evidence changes binds nothing: it
// names the question, not the answer.
func TestTheEvidenceDigestChangesWhenTheEvidenceChanges(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	base := RouteAuthorizationWitness{
		CedarAction:    "session:stop",
		ScopedEffect:   EffectGrant,
		ResourceDigest: ResourceDigest("t1", ResourceAttrs{Kind: "session", ID: "s1"}),
		PolicyVersion:  7,
		Decision: AuthorizationEvidence{
			Outcome:        EvidenceAllow,
			CorePermission: CheckEvidence{Verdict: CheckClean, Code: "rbac_permitted"},
			ResourceGuard:  CheckEvidence{Verdict: CheckClean, Code: "guard_clean"},
			ForbidAbsence:  CheckEvidence{Verdict: CheckClean, Code: "no_forbid"},
			Facts:          []store.AuthorizationFactRef{{Kind: "grant", ID: "g1", Version: 7}},
			ObservedAt:     now,
			FreshUntil:     now.Add(time.Minute),
		},
	}
	seen := map[[32]byte]string{evidenceDigest(base): "base"}
	mutations := []struct {
		name string
		bend func(*RouteAuthorizationWitness)
	}{
		{"outcome", func(w *RouteAuthorizationWitness) { w.Decision.Outcome = EvidenceDeny }},
		{"scoped effect", func(w *RouteAuthorizationWitness) { w.ScopedEffect = EffectForbid }},
		{"core verdict", func(w *RouteAuthorizationWitness) {
			w.Decision.CorePermission.Verdict = CheckBroken
		}},
		{"guard verdict", func(w *RouteAuthorizationWitness) {
			w.Decision.ResourceGuard.Verdict = CheckUnknown
		}},
		{"forbid verdict", func(w *RouteAuthorizationWitness) {
			w.Decision.ForbidAbsence.Verdict = CheckUnknown
		}},
		{"fact kind", func(w *RouteAuthorizationWitness) {
			w.Decision.Facts = []store.AuthorizationFactRef{{Kind: "role", ID: "g1", Version: 7}}
		}},
		{"fact id", func(w *RouteAuthorizationWitness) {
			w.Decision.Facts = []store.AuthorizationFactRef{{Kind: "grant", ID: "g2", Version: 7}}
		}},
		{"fact count", func(w *RouteAuthorizationWitness) {
			w.Decision.Facts = append(w.Decision.Facts,
				store.AuthorizationFactRef{Kind: "grant", ID: "g9", Version: 3})
		}},
		{"observed at", func(w *RouteAuthorizationWitness) {
			w.Decision.ObservedAt = now.Add(time.Second)
		}},
		{"fresh until", func(w *RouteAuthorizationWitness) {
			w.Decision.FreshUntil = now.Add(2 * time.Minute)
		}},
		{"cedar action", func(w *RouteAuthorizationWitness) { w.CedarAction = "session:input" }},
		{"resource", func(w *RouteAuthorizationWitness) {
			w.ResourceDigest = ResourceDigest("t1", ResourceAttrs{Kind: "session", ID: "s2"})
		}},
		{"policy version", func(w *RouteAuthorizationWitness) { w.PolicyVersion = 8 }},
	}
	for _, m := range mutations {
		w := base
		w.Decision.Facts = append([]store.AuthorizationFactRef(nil), base.Decision.Facts...)
		m.bend(&w)
		d := evidenceDigest(w)
		if prev, dup := seen[d]; dup {
			t.Errorf("changing %s left the digest identical to %s: the field that claims to "+
				"bind the evidence does not observe it", m.name, prev)
		}
		seen[d] = m.name
	}
	// And the digest is stable for the same witness: one that varied per call would make
	// every comparison against a stored one fail.
	if evidenceDigest(base) != evidenceDigest(base) {
		t.Fatal("the digest is not stable across calls")
	}
}

// TestVerifyForAsksTheWholeQuestion is the positive side of R-630, and it lives here because
// this is the only package where a sealed witness can exist.
//
// ⛔ FIVE TERMS, AND THE FIFTH IS THE ONE A SEAL CANNOT GIVE YOU. The first four say the witness
// is genuine, unedited, still valid and about this row. None of them says it answered the
// question being ASKED — so an AUTHENTIC witness minted for one principal, permission and action
// was, until QuestionDigest, perfectly good evidence for a different one over the same row. No
// forgery: a transplant.
func TestVerifyForAsksTheWholeQuestion(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	res := ResourceAttrs{Kind: "session", ID: "s1"}
	other := ResourceAttrs{Kind: "session", ID: "s2"}

	pregunta := func(user model.ID, perm Permission, r ResourceAttrs) Request {
		return Request{
			Principal:  Principal{Kind: KindUser, AAL: AAL3, UserID: user},
			Permission: perm,
			Tenant:     "t1",
			Resource:   r,
			Route:      RouteMetadata{CedarAction: "session:stop"},
		}
	}
	mint := func(req Request) RouteAuthorizationWitness {
		w := RouteAuthorizationWitness{
			CedarAction:    resolveCedarAction(req),
			ScopedEffect:   EffectGrant,
			ResourceDigest: ResourceDigest(req.Tenant, req.Resource),
			QuestionDigest: questionDigest(req),
			PolicyVersion:  7,
			Decision: AuthorizationEvidence{
				Outcome:        EvidenceAllow,
				CorePermission: CheckEvidence{Verdict: CheckClean, Code: "rbac_permitted"},
				ResourceGuard:  CheckEvidence{Verdict: CheckClean, Code: "guard_clean"},
				ForbidAbsence:  CheckEvidence{Verdict: CheckClean, Code: "no_forbid"},
				ObservedAt:     now.Add(-time.Second),
				FreshUntil:     now.Add(time.Minute),
			},
			minted: true,
		}
		w.EvidenceDigest = evidenceDigest(w)
		return w
	}
	qA := pregunta("uA", "session-cockpit:stop:write", res)

	// CONTROL POSITIVO PRIMERO: sin él, cada rechazo de abajo lo satisface un VerifyFor que
	// devuelva false siempre, que es la forma que toma una guarda rota.
	if !mint(qA).VerifyFor(now, qA) {
		t.Fatal("un testigo acuñado para esta pregunta exacta no verifica")
	}

	// ⛔ EL TRASPLANTE, que es el hallazgo entero en una línea. El testigo es REAL: acuñado,
	// sin editar, dentro de ventana y sobre ESTA fila. Lo único que cambia es de quién y para
	// qué se preguntó.
	for _, caso := range []struct {
		nombre string
		q      Request
	}{
		{"otro principal", pregunta("uB", "session-cockpit:stop:write", res)},
		{"otro permiso", pregunta("uA", "session-cockpit:read:read", res)},
		{"otra fila", pregunta("uA", "session-cockpit:stop:write", other)},
	} {
		if mint(qA).VerifyFor(now, caso.q) {
			t.Errorf("%s: un testigo auténtico de OTRA pregunta verificó; no hace falta falsificarlo, "+
				"basta con presentarlo donde no toca", caso.nombre)
		}
	}
	// Y la acción, que no viaja en el permiso: misma pregunta, otro Action ID declarado.
	qAccion := qA
	qAccion.Route = RouteMetadata{CedarAction: "session:delete"}
	if mint(qA).VerifyFor(now, qAccion) {
		t.Error("un testigo de `session:stop` verificó para `session:delete`: la acción no se deriva " +
			"del permiso, así que nada más la habría comparado")
	}

	// ORIGEN: todo lo demás en su sitio y nadie lo acuñó.
	sinSello := mint(qA)
	sinSello.minted = false
	if sinSello.VerifyFor(now, qA) {
		t.Error("un testigo que nadie acuñó verificó")
	}

	// INTEGRIDAD: real, copiado y editado después. El sello viaja con la copia; sólo recomputar
	// el digest lo ve.
	tocado := mint(qA)
	tocado.CedarAction = "session:delete"
	if tocado.VerifyFor(now, qA) {
		t.Error("un testigo acuñado y editado después verificó")
	}
	// Y el digest de la PREGUNTA también está dentro del de evidencia: editarlo se nota.
	//
	// ⛔ ESTE CASO FALLÓ, Y SU MENSAJE CULPABA AL SITIO EQUIVOCADO. Decía «no está en el digest
	// de evidencia», y sí estaba (`evidenceDigest` escribe `w.QuestionDigest`). Lo que pasaba es
	// que la «mutación» no mutaba NADA: `questionDigest` colisionaba para uA y uB, así que el
	// testigo quedaba byte a byte idéntico al original y verificaba con razón. Un mensaje de
	// fallo que afirma una causa se cree igual que un comentario, y éste habría mandado a
	// arreglar una función correcta.
	tocado2 := mint(qA)
	tocado2.QuestionDigest = questionDigest(pregunta("uB", "session-cockpit:stop:write", res))
	if tocado2.VerifyFor(now, qA) {
		t.Error("un testigo real con el QuestionDigest editado verificó: o el digest de evidencia " +
			"no lo cubre, o la edición no cambió nada porque las dos preguntas colisionan")
	}

	// VENTANA, los DOS extremos. Sin el inferior, un testigo cuya decisión aún no se ha tomado
	// autorizaría hoy.
	if mint(qA).VerifyFor(now.Add(2*time.Minute), qA) {
		t.Error("un testigo verificó después de cerrarse su ventana")
	}
	if mint(qA).VerifyFor(now.Add(-time.Minute), qA) {
		t.Error("un testigo verificó ANTES de que su decisión se observara: una ventana con un solo " +
			"extremo dice «válida desde siempre», que ninguna evaluación puede afirmar")
	}

	// TENANT: mismo recurso, otro dueño.
	qT := qA
	qT.Tenant = "t2"
	if mint(qA).VerifyFor(now, qT) {
		t.Error("un testigo sellado para t1 verificó sirviendo a t2")
	}
}

// TestAGovernedRouteMintsAWitnessForAPrincipalWithEvidence is the case that decides whether the
// cockpit can be deployed at all, and it carries a second question only visible here.
//
// ⛔ FIRST: CAN A WITNESS EVER BE MINTED? Every governed route answers 503 today, and the reason
// is not policy — it is that AuthorizeEvidence wants a sealed, windowed directory-epoch fact for
// the principal and nothing on the HTTP path installs one. That makes "the route mints a witness"
// unfalsifiable through the server, so it is asked HERE, where ResolvePrincipalScope installs the
// evidence the way production will have to.
//
// ⛔ SECOND, AND IT IS ABOUT THE CURE ITSELF: DOES THE WINDOW CHECK DENY A GOOD DECISION?
// AuthorizeRoute refuses a witness whose decision window does not contain the present. That check
// is what stops a stale-but-authentic decision from authorizing, and it is also the exact shape of
// a security cure that becomes an outage. It runs here against the REAL clock and a principal
// resolved from the REAL database clock, because that is the pairing production has. Passing it
// with WithClock would be fitting the test to the code: if the window a legitimate resolution
// produces does not contain the present, production is broken and this must say so rather than
// move the clock until it agrees.
func TestAGovernedRouteMintsAWitnessForAPrincipalWithEvidence(t *testing.T) {
	f, principal := resolvedPrincipalAuthorityEvidence(t)
	req := principalAuthorityEvidenceRequest(principal, f.tenant)
	req.Route = RouteMetadata{CedarAction: "agent:read"}

	az := NewAuthorizer(nil)
	w, err := az.AuthorizeRoute(context.Background(), req)
	if err != nil {
		t.Fatalf("a principal carrying resolved authority evidence got no witness: %v\n"+
			"AuthorizeEvidence reports ALLOW for this exact request (see "+
			"TestPrincipalAuthorityEvidenceResolvedPrincipalAllows), so a refusal here is "+
			"AuthorizeRoute's own — most likely the window check, if the decision window taken "+
			"from the database clock does not contain the present.", err)
	}

	// A witness that EXISTS is not a witness that ANSWERS. VerifyFor re-derives both digests and
	// the window, which is what makes it evidence rather than a token.
	if !w.VerifyFor(time.Now(), req) {
		t.Fatal("the minted witness does not verify for the very question it was minted for")
	}

	// ⛔ THE CONTROL POSITIVE, AND IT IS THE MUTANT THAT MATTERS. Without it this passes on a
	// server that mints witnesses for everyone, which is strictly worse than one that mints none.
	// Removing ONLY the private provenance — same principal, same roles, same permission — must
	// take the answer from "allowed, here is the witness" to "I could not decide".
	bare := req
	bare.Principal.evidence = principalEvidenceProvenance{}
	if _, err := az.AuthorizeRoute(context.Background(), bare); !errors.Is(err, ErrRouteUndecided) {
		t.Fatalf("a principal WITHOUT authority evidence got %v, want ErrRouteUndecided\n"+
			"Unknown is not denial: nothing said no, the evidence simply is not there, and "+
			"serving that as a denial is the confusion the third state exists to end.", err)
	}
}

// TestASoundWitnessCanStillAnswerAnotherQuestion pins the STATE that api's
// ErrRowQuestionMismatch exists for, in the only package where that state can be built.
//
// ⛔ AND IT IS HERE BECAUSE IT CANNOT BE THERE. `minted` is unexported and only AuthorizeRoute
// sets it, so no witness constructed outside this package can ever be sound — which means a test
// in core/api takes the !IsSound branch every time and can never reach the second one. That is
// the seal working, not a gap in it, and it is why the pair is asserted here instead: a witness
// that IS sound and answers a DIFFERENT question is exactly the input CheckRowSet must report as
// "another question" rather than as tampering.
//
// ⚠ WHAT THIS DOES NOT PROVE, said so nobody inherits it as covered: that CheckRowSet MAPS this
// state to ErrRowQuestionMismatch. That needs a sound witness inside core/api, which needs a
// principal carrying sealed evidence, which nothing on the HTTP path installs yet. The mapping
// gets its witness in the lane that wires principal evidence — where the branch becomes
// reachable in production for the first time, and where a green there will mean something.
func TestASoundWitnessCanStillAnswerAnotherQuestion(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	tenant := model.TenantID("t1")
	res := ResourceAttrs{Kind: "session", ID: "s1"}
	ask := func(user string) Request {
		return Request{
			Principal:  Principal{Kind: KindUser, AAL: AAL3, UserID: model.ID(user)},
			Permission: "session-cockpit:stop:write", Tenant: tenant, Resource: res,
			Route: RouteMetadata{CedarAction: "session:stop"},
		}
	}
	qA, qB := ask("uA"), ask("uB")

	w := RouteAuthorizationWitness{
		CedarAction:    resolveCedarAction(qA),
		ScopedEffect:   EffectGrant,
		ResourceDigest: ResourceDigest(qA.Tenant, qA.Resource),
		QuestionDigest: questionDigest(qA),
		PolicyVersion:  7,
		Decision: AuthorizationEvidence{
			Outcome:        EvidenceAllow,
			CorePermission: CheckEvidence{Verdict: CheckClean, Code: "rbac_permitted"},
			ResourceGuard:  CheckEvidence{Verdict: CheckClean, Code: "guard_clean"},
			ForbidAbsence:  CheckEvidence{Verdict: CheckClean, Code: "no_forbid"},
			ObservedAt:     now.Add(-time.Second),
			FreshUntil:     now.Add(time.Minute),
		},
		minted: true,
	}
	w.EvidenceDigest = evidenceDigest(w)

	// Sound: it holds up on its own. This is the half that must NOT be what a consumer reports.
	if !w.IsSound(now) {
		t.Fatal("a minted, in-window, unedited witness is not sound: the two halves cannot be " +
			"separated if the first one is wrong")
	}
	// And about something else. Same row, same permission, same action — a different asker.
	if w.AnswersQuestion(qB) {
		t.Fatal("a witness minted for one principal answers another's question: the state this " +
			"pair exists to name does not exist, and the error that names it guards nothing")
	}
	// The control positive, without which a broken AnswersQuestion that always returns false
	// satisfies the line above.
	if !w.AnswersQuestion(qA) {
		t.Fatal("the witness does not answer the question it was minted for")
	}
}

// TestTheQuestionDigestBindsEveryDecisiveRouteField is HIGH-3 of the third contrast, measured
// field by field.
//
// ⛔ EL AGUJERO ERA UNA ESCALADA, NO UNA IMPRECISIÓN. El digest ataba la acción de la ruta y nada
// más de su metadata, así que un testigo AUTÉNTICO acuñado en la ruta que concede FÁCIL —sin grant
// scoped, sin suelo de rol, sin suelo de AAL— verificaba en la que concede DIFÍCIL, con el mismo
// actor, el mismo permiso, la misma acción y la misma fila. No hacía falta falsificarlo: bastaba
// con presentarlo donde no tocaba.
//
// ⛔ Y SE COMPRUEBAN DE UNO EN UNO A PROPÓSITO. Cambiar varios a la vez pasa con que el digest ate
// UNO cualquiera de ellos, que es exactamente el estado que teníamos y que un caso "cambio toda la
// metadata" habría declarado sano. Cada campo es su propio mutante.
func TestTheQuestionDigestBindsEveryDecisiveRouteField(t *testing.T) {
	base := Request{
		Principal:  Principal{Kind: KindUser, AAL: AAL1, UserID: "uA"},
		Permission: "session-cockpit:stop:write",
		Tenant:     "t1",
		Resource:   ResourceAttrs{Kind: "session", ID: "s1"},
		Route:      RouteMetadata{CedarAction: "session:stop"},
	}
	want := questionDigest(base)

	for _, c := range []struct {
		nombre string
		muta   func(*Request)
	}{
		{"exige grant scoped", func(r *Request) { r.Route.RequireScopedGrant = true }},
		{"suelo de rol RBAC", func(r *Request) { r.Route.RBACMinimumRole = "admin" }},
		{"hereda grupos del agente", func(r *Request) { r.Route.SessionInheritsAgentGroups = true }},
		{"suelo de AAL", func(r *Request) { r.Route.MinimumAAL = 3 }},
		{"AAL efectivo del que pregunta", func(r *Request) { r.Principal.AAL = AAL3 }},
		{"sensibilidad del recurso", func(r *Request) { r.Resource.Sensitivity = "restricted" }},
	} {
		otra := base
		c.muta(&otra)
		if questionDigest(otra) == want {
			t.Errorf("%s: la pregunta cambió y el digest NO. Un testigo acuñado sin ese término "+
				"verifica para una ruta que sí lo exige: misma persona, mismo permiso, misma "+
				"acción, misma fila, y una decisión distinta", c.nombre)
		}
	}

	// ⛔ CONTROL POSITIVO, sin el cual todo lo de arriba lo satisface un digest aleatorio: la
	// MISMA pregunta, construida dos veces, da el MISMO digest. Sin esto, un `questionDigest` que
	// devolviera bytes distintos en cada llamada pasaría los seis casos y no verificaría nunca
	// nada — el fallo se leería como «el testigo no vale» en vez de «la sonda está rota».
	if questionDigest(base) != want {
		t.Fatal("la misma pregunta da dos digests distintos: nada verificaría jamás, y el síntoma " +
			"sería indistinguible de un testigo manipulado")
	}
}

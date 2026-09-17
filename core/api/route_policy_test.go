// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// TestRouteMetadataZeroIsTheHistoricalBehaviour is the back-compat pin. Every route that
// has not opted in must decide bit-for-bit as it did before this type existed, and the
// field that would break it silently is SessionInheritsAgentGroups.
func TestRouteMetadataZeroIsTheHistoricalBehaviour(t *testing.T) {
	var m RouteMetadata
	if !m.IsZero() {
		t.Fatal("the zero RouteMetadata does not read as zero")
	}
	if m.SessionInheritsAgentGroups {
		t.Fatal("SessionInheritsAgentGroups defaults to TRUE: the scope resolver is global, " +
			"so this would move authorization for every caller in the engine, including " +
			"AuthZEN per row and access-review")
	}
	if m.RequireScopedGrant || m.RBACMinimumRole != "" || m.MinimumAAL != 0 || m.CedarAction != "" {
		t.Error("a zero RouteMetadata carries a non-zero requirement")
	}
	if m.CoreKind != CoreKindNone {
		t.Error("a zero RouteMetadata names a core entity kind")
	}
}

// TestValidateRejectsAAL2 pins the level the engine does not define. A route asking for 2
// would deny every AAL1 caller and admit every AAL3 one, which is neither of the two
// things its author could have meant.
func TestValidateRejectsAAL2(t *testing.T) {
	m := RouteMetadata{RouteMetadata: auth.RouteMetadata{MinimumAAL: 2}}
	err := m.Validate()
	if err == nil {
		t.Fatal("MinimumAAL 2 validated: the engine defines AAL1 and AAL3 only")
	}
	if !strings.Contains(err.Error(), "MinimumAAL 2") {
		t.Errorf("the error does not name the level: %v", err)
	}
	// Control positive: the three levels the engine DOES define are accepted.
	for _, aal := range []int{0, auth.AAL1, auth.AAL3} {
		ok := RouteMetadata{RouteMetadata: auth.RouteMetadata{MinimumAAL: aal}}
		if err := ok.Validate(); err != nil {
			t.Errorf("MinimumAAL %d rejected: %v", aal, err)
		}
	}
}

// TestValidateRejectsScopedGrantWithoutAction: a route that requires a grant but names no
// action would have the grant evaluated against an action derived from the permission,
// which is not the action the route means.
func TestValidateRejectsScopedGrantWithoutAction(t *testing.T) {
	m := RouteMetadata{RouteMetadata: auth.RouteMetadata{RequireScopedGrant: true}}
	if err := m.Validate(); err == nil {
		t.Fatal("a scoped-grant route with no Cedar action validated")
	}
	ok := RouteMetadata{RouteMetadata: auth.RouteMetadata{RequireScopedGrant: true, CedarAction: "session:stop"}}
	if err := ok.Validate(); err != nil {
		t.Errorf("a scoped-grant route WITH an action was rejected: %v", err)
	}
}

// TestValidateRejectsARoleFloorTheEngineDoesNotKnow is the typo half of HIGH-4, found by the
// different-engine contrast on 2026-09-04.
//
// ⛔ AN UNKNOWN ROLE DOES NOT DENY — IT ADMITS. RoleRank returns 0 for anything it does not
// know, and rbacPermitted denies only when the caller's rank is BELOW the floor's. So a floor
// of "editorr" ranks 0, no real role ranks below 0, and the restriction disappears while the
// route mounts and serves normally. A reader sees a route with an RBAC floor; the engine sees
// a route with none, and nothing in between reports the difference.
func TestValidateRejectsARoleFloorTheEngineDoesNotKnow(t *testing.T) {
	bad := RouteMetadata{RouteMetadata: auth.RouteMetadata{RBACMinimumRole: "editorr"}}
	if err := bad.Validate(); err == nil {
		t.Fatal(`a route declared a floor of "editorr" and mounted: that floor ranks 0 and denies nobody`)
	}

	// ⛔ THE PROPERTY THAT MAKES THE CHECK NECESSARY, ASSERTED RATHER THAN DESCRIBED. The
	// paragraph above is only true while an unknown role ranks 0 AND no known role ranks below
	// it. Both are pinned here, so the day RoleRank changes shape this fails loudly instead of
	// leaving a guard whose reason has quietly stopped applying.
	if auth.RoleRank("editorr") != 0 {
		t.Errorf("an unknown role ranks %d, not 0: the failure mode this guards has changed shape",
			auth.RoleRank("editorr"))
	}
	for _, r := range []string{auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin, auth.RoleOwner} {
		if auth.RoleRank(r) < auth.RoleRank("editorr") {
			t.Errorf("the known role %q ranks below an unknown one: a mistyped floor would have "+
				"denied after all, and this guard is solving a problem that no longer exists", r)
		}
		// CONTROL POSITIVE: every role the engine DOES know is accepted, so this test cannot
		// pass against a Validate that simply refuses every floor.
		known := RouteMetadata{RouteMetadata: auth.RouteMetadata{RBACMinimumRole: r}}
		if err := known.Validate(); err != nil {
			t.Errorf("the control failed: a route with the known floor %q was rejected: %v", r, err)
		}
	}

	// And an empty floor stays legal, because it is what every route that has not opted in
	// declares — the zero metadata has to keep validating or this check would ground the fleet.
	if err := (RouteMetadata{}).Validate(); err != nil {
		t.Errorf("the zero metadata was rejected: %v", err)
	}
}

// TestCheckRowSetDeniesAMismatchedZip is the guard whose absence is invisible.
//
// A verdict slice shorter than the candidate slice does not crash: it pairs each row with
// its NEIGHBOUR's verdict for as far as it goes, so some caller sees a row it may not see
// and the page still looks well formed.
//
// ⛔ THIS PACKAGE CANNOT BUILD A WITNESS THAT VERIFIES, AND THAT IS THE SEAL WORKING. A witness
// carries an unexported mark that only auth.AuthorizeRoute sets, and minting one for real needs
// a Principal carrying authority evidence — also unexported, and installable only inside
// core/auth. So every "allowed" row typed here is refused by design, and the positive side of
// the guard is tested where a sealed witness can exist: core/auth's TestVerifyForAsksTheWhole-
// Question. What stays here is what belongs here — the arithmetic, the refusals, and a page
// that must be SERVED so a CheckRowSet that denied everything could not pass.
func TestCheckRowSetDeniesAMismatchedZip(t *testing.T) {
	const tenant = model.TenantID("t1")
	// La PREGUNTA que estos casos hacen. CheckRowSet ya no recibe un tenant: recibe la pregunta
	// entera, y el recurso de cada fila se pone encima. Un testigo autentico de OTRA pregunta
	// la misma fila pasaba todo lo demas.
	req := auth.Request{Principal: auth.Principal{Kind: auth.KindUser, AAL: auth.AAL3, UserID: "u1"},
		Permission: "session-cockpit:stop:write", Tenant: tenant,
		Route: auth.RouteMetadata{CedarAction: "session:stop"}}
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	rows := []auth.ResourceAttrs{{Kind: "session", ID: "s1"}, {Kind: "session", ID: "s2"}}

	// CONTROL POSITIVE, and it goes first: a page where the caller may see NOTHING is still a
	// well-formed page, and it is served. Without this case a CheckRowSet that returned an
	// error unconditionally would pass every other assertion in this test.
	allDenied := AuthorizedRowSet{
		Allowed:   []bool{false, false},
		Witnesses: make([]auth.RouteAuthorizationWitness, 2),
	}
	if err := CheckRowSet(allDenied, now, req, rows); err != nil {
		t.Fatalf("a page of rows the caller may not see was refused: %v", err)
	}

	three := []auth.ResourceAttrs{rows[0], rows[1], {Kind: "session", ID: "s3"}}
	if err := CheckRowSet(allDenied, now, req, three); err == nil {
		t.Fatal("three rows against two verdicts was accepted: each row would be paired " +
			"with its neighbour's verdict")
	} else if !errors.Is(err, ErrRowDecisionMismatch) {
		t.Errorf("the error is not the typed one: %v", err)
	}
	// And the other direction: more verdicts than rows is equally a mismatch.
	if err := CheckRowSet(allDenied, now, req, rows[:1]); err == nil {
		t.Fatal("one row against two verdicts was accepted")
	}

	// ⛔ THE TWO SLICES ARE CHECKED SEPARATELY, and the cases above cannot tell. Every one of
	// them moves `rows` while Allowed and Witnesses stay the same length as each other, so
	// deleting either half of the condition leaves them all green: with only the Allowed term,
	// a set carrying the right number of VERDICTS and one witness too few is accepted, and the
	// pager then hands row 1 the witness of row 0. A witness is the record of WHY a row was
	// shown; the wrong one is not a missing detail, it is an authorization attributed to a
	// decision that was taken about something else.
	shortWitnesses := AuthorizedRowSet{
		Allowed:   []bool{false, false},
		Witnesses: make([]auth.RouteAuthorizationWitness, 1),
	}
	if err := CheckRowSet(shortWitnesses, now, req, rows); err == nil {
		t.Error("two rows with two verdicts and ONE witness was accepted: the second row " +
			"would be rendered carrying the first row's witness")
	}
	shortAllowed := AuthorizedRowSet{
		Allowed:   []bool{false},
		Witnesses: make([]auth.RouteAuthorizationWitness, 2),
	}
	if err := CheckRowSet(shortAllowed, now, req, rows); err == nil {
		t.Error("two rows with ONE verdict and two witnesses was accepted")
	}
}

// TestAWitnessThisPackageTypedIsRefused is R-630 seen from the consumer's side.
//
// ⛔ THE OLD GUARD COULD NOT TELL THIS FROM A REAL DECISION. It compared ResourceDigest by hand,
// and a value typed right here with the correct digest satisfied it — so the page carried an
// authorization that no authorizer had ever produced, about a row that may never have been
// evaluated. The digest proves the SUBJECT; nothing proved the ORIGIN.
//
// Every witness in this test is one this package built, which is precisely the point: after the
// seal, none of them can be honoured, however well filled in. The case that would fail loudest
// if the seal were removed is the first one, because it is filled in exactly as the old guard
// wanted.
func TestAWitnessThisPackageTypedIsRefused(t *testing.T) {
	const tenant = model.TenantID("t1")
	// La PREGUNTA que estos casos hacen. CheckRowSet ya no recibe un tenant: recibe la pregunta
	// entera, y el recurso de cada fila se pone encima. Un testigo autentico de OTRA pregunta
	// la misma fila pasaba todo lo demas.
	req := auth.Request{Principal: auth.Principal{Kind: auth.KindUser, AAL: auth.AAL3, UserID: "u1"},
		Permission: "session-cockpit:stop:write", Tenant: tenant,
		Route: auth.RouteMetadata{CedarAction: "session:stop"}}
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	rows := []auth.ResourceAttrs{{Kind: "session", ID: "s1"}, {Kind: "session", ID: "s2"}}

	// Filled in the way the old guard asked for: right resource, allowing outcome, open window.
	forged := AuthorizedRowSet{
		Allowed: []bool{true, false},
		Witnesses: []auth.RouteAuthorizationWitness{
			{
				ResourceDigest: auth.ResourceDigest(tenant, rows[0]),
				Decision: auth.AuthorizationEvidence{
					Outcome:    auth.EvidenceAllow,
					ObservedAt: now,
					FreshUntil: now.Add(time.Minute),
				},
			},
			{},
		},
	}
	err := CheckRowSet(forged, now, req, rows)
	if err == nil {
		t.Fatal("a witness typed in this package, with the correct ResourceDigest and an open " +
			"window, was honoured: nothing minted it, so nothing decided this row")
	}
	if !errors.Is(err, ErrRowWitnessUnverified) {
		t.Errorf("the error is not the typed one: %v", err)
	}

	// And at index 0 as well as index 1 — the one index a loop written `for i := 1` omits.
	// Here the ONLY allowed row IS index 0, so a guard that skipped it would return nil.
	if err := CheckRowSet(forged, now, req, rows); err == nil {
		t.Error("the forged witness sat at index 0 and was not looked at")
	}

	// CONTROL POSITIVE: the same shape with that row DENIED is served, because a denial carries
	// no witness by construction. Without this, a CheckRowSet that refused every page would
	// satisfy every assertion above.
	denied := forged
	denied.Allowed = []bool{false, false}
	if err := CheckRowSet(denied, now, req, rows); err != nil {
		t.Errorf("a page whose rows are all denied was refused: %v", err)
	}
}

// TestNilAuthorizerDeniesRatherThanAllows: a route whose row port was never installed
// must not serve rows. The failure direction of a missing dependency is the whole test.
func TestNilAuthorizerDeniesRatherThanAllows(t *testing.T) {
	port := NewRowAuthorizationPort(nil)
	set, err := port.DecideRows(t.Context(), auth.Principal{}, "t1", "x:y:read",
		RouteMetadata{}, []auth.ResourceAttrs{{Kind: "session", ID: "a"}})
	if err == nil {
		t.Fatal("a nil authorizer decided rows: an uninstalled port must not serve them")
	}
	if !errors.Is(err, auth.ErrAuthorizerUnavailable) {
		t.Errorf("the error is not the 'could not look' one: %v", err)
	}
	if len(set.Allowed) != 0 {
		t.Errorf("a failed decision still returned %d verdicts", len(set.Allowed))
	}
	actions, err := port.DecideActions(t.Context(), auth.Principal{}, "t1",
		auth.ResourceAttrs{Kind: "session", ID: "a"}, []RouteActionRequirement{{Permission: "x:y:write"}})
	if err == nil {
		t.Fatal("a nil authorizer decided actions")
	}
	if len(actions.Actions) != 0 {
		t.Error("a failed action decision still offered actions")
	}
}

// TestAuthenticationFactsOmitsAuthenticatedAt is a test about an ABSENCE, and it exists so
// the absence stays deliberate.
//
// The architecture wants a five-minute freshness on the AAL3 ceremony. The engine cannot
// answer it today and must not invent it: a zero timestamp in a struct called
// "AuthenticationFacts" reads as "authenticated at the epoch" to every caller that forgets
// the zero check, which either always denies or, with the comparison written the other
// way, always passes. When model.AuthSession gains AALAuthenticatedAt, this test is the
// one to change.
func TestAuthenticationFactsOmitsAuthenticatedAt(t *testing.T) {
	f := AuthenticationFactsOf(auth.Principal{AAL: auth.AAL3})
	if f.AAL != auth.AAL3 {
		t.Errorf("AAL = %d, want %d", f.AAL, auth.AAL3)
	}
	if f.HasCredential {
		t.Error("a synthetic principal reported an established credential reference")
	}
}

// TestTheConcreteRegistrarCarriesPolicy is finding B01's fix, measured on the type rather
// than on a comment.
//
// The module that needs the metadata detects it by ASSERTION, so what has to be true is that
// chiRegistrar satisfies the shape. A test that only read the method's body would pass for a
// method nobody can find.
func TestTheConcreteRegistrarCarriesPolicy(t *testing.T) {
	type policyRegistrar interface {
		HandlePolicy(method, pattern string, perm auth.Permission, meta RouteMetadata, h ModuleHandler)
	}
	var reg RouteRegistrar = chiRegistrar{}
	if _, ok := reg.(policyRegistrar); !ok {
		t.Fatal("chiRegistrar does not satisfy the policy-carrying shape, so a module asking " +
			"for it by assertion finds nothing and withholds every governed route")
	}
	// And the plain registrar interface is UNCHANGED: adding the method there would break
	// every implementer, test doubles included, for a capability most modules do not want.
	var _ RouteRegistrar = chiRegistrar{}
}

// TestZeroMetadataLeavesEveryRBACTermInPlace measures the three arms of the metadata, and its
// NAME now says so — which is the whole correction.
//
// ⛔ IT USED TO BE CALLED TestZeroMetadataIsBitForBitTheOldFlow, AND IT PASSED WHILE THAT FLOW
// WAS BROKEN IN FIFTY TESTS. Nothing in it was wrong: the three assertions below were true
// then and are true now. The name promised an equivalence of the FLOW and the body measured a
// property of the STRUCT, so when the two doors stopped calling the same authorization function
// — one moved to AuthorizeRoute, which demands sealed principal evidence the other never asked
// for — the divergence appeared on an axis this test does not look at, and the green stayed
// green.
//
// ⇒ A test's name is an assertion too, and it is the one a reader trusts without opening the
// body. This one is now named after what it can actually see. What it CANNOT see — that the two
// doors still answer the same question — is measured where it is observable, end to end, in
// TestTheGovernedDoorReachesAuthorizeRouteEvenWithZeroMetadata.
func TestZeroMetadataLeavesEveryRBACTermInPlace(t *testing.T) {
	var m auth.RouteMetadata
	if !m.IsZero() {
		t.Fatal("the zero metadata is not zero")
	}
	// MinimumAAL 0 means the precondition never fires, whatever the principal's AAL.
	if m.MinimumAAL != 0 {
		t.Error("the zero metadata demands an assurance level")
	}
	// RequireScopedGrant false leaves the RBAC term in place: the flag only ever REMOVES it.
	if m.RequireScopedGrant {
		t.Error("the zero metadata removes the RBAC term")
	}
	// And the global resolver consults inheritance only when it is true.
	if m.SessionInheritsAgentGroups {
		t.Error("the zero metadata inherits agent groups, which would move authorization for " +
			"every caller of the global scope resolver")
	}
	// ⛔ Y LOS DOS QUE FALTABAN, porque el nombre dice «EVERY RBAC term» y el cuerpo miraba tres.
	// RBACMinimumRole vacio es el que de verdad hace este nombre verdadero: un suelo de rol
	// desconocido rankea 0 y ADMITE A TODO EL MUNDO, asi que su cero tiene que ser «sin suelo» y
	// no «suelo que no reconozco».
	if m.RBACMinimumRole != "" {
		t.Error("the zero metadata carries an RBAC role floor")
	}
	// Y la accion: vacia cae al permiso (modules/governance actionUID), que es el comportamiento
	// de todas las rutas del arbol hoy.
	if m.CedarAction != "" {
		t.Error("the zero metadata names a Cedar action, so the route would be decided against an " +
			"action entity instead of its permission")
	}
	// ⚠ Y MinimumAAL NO SE COMPRUEBA AQUI AUNQUE SEA CERO: no es un termino RBAC. El step-up es
	// una precondicion de autenticacion y vive fuera del algebra (middleware.go lo dice donde lo
	// aplica); meterlo en un caso que se llama «every RBAC term» seria volver a que el nombre
	// prometa un eje que el cuerpo no mide, por el otro lado.
}

// TestTighteningZeroIsRefusedRatherThanDereferenced: `Tightening{}` es construible —un literal a
// medio rellenar, un elemento de slice sin inicializar— y su `apply` es nil.
//
// ⛔ ANTES TIRABA EL PROCESO EN EL REGISTRO DE RUTAS. Un boot que muere con un nil pointer no dice
// cual de los endurecimientos estaba vacio, y un endurecimiento que no endurece nada tampoco es un
// no-op benigno: es una linea que el lector cuenta como restriccion aplicada.
func TestTighteningZeroIsRefusedRatherThanDereferenced(t *testing.T) {
	sealed, err := SealRoute(RouteMetadata{RouteMetadata: auth.RouteMetadata{CedarAction: "a:b"}})
	if err != nil {
		t.Fatalf("SealRoute: %v", err)
	}
	if _, err := sealed.Tighten(Tightening{}); err == nil {
		t.Fatal("a zero Tightening was applied: it restricts nothing and reads like a restriction")
	}
	// CONTROL POSITIVO: un endurecimiento REAL sigue aplicandose, o el caso de arriba lo satisface
	// un Tighten que rechace siempre.
	if _, err := sealed.Tighten(RaiseMinimumAAL(3)); err != nil {
		t.Fatalf("a real tightening was refused: %v", err)
	}
}

// TestHandlePolicyRefusesImpossibleMetadataAtMount: registration runs at boot, so an
// impossible route is a refusal to START rather than a surprise at the first request.
func TestHandlePolicyRefusesImpossibleMetadataAtMount(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("HandlePolicy accepted metadata that boot validation rejects")
		}
	}()
	cr := chiRegistrar{ns: "test"}
	cr.HandlePolicy("GET", "/x", "a:b:read",
		RouteMetadata{RouteMetadata: auth.RouteMetadata{MinimumAAL: 2}},
		func(http.ResponseWriter, *http.Request, ModuleContext) {})
}

// B01 of the 2026-09-03 contrast: a route could declare a CoreKind and be mounted with no
// way for the engine to find its row.
//
// ⛔ THE FAILURE WORE THE SHAPE OF AN AUTHORIZATION DECISION, which is the one disguise a
// reader cannot see through. `HandlePolicy` built `&EntityRef{CoreKind: ...}` and the engine
// requires exactly one of IDParam and BodyIDField before it will look a row up - so it
// denied BEFORE the handler, every time, and the route was mounted, reachable and unusable.
// Fourteen of them in the session cockpit, each looking like a permission problem.

// TestACoreKindWithoutALocatorIsRefusedAtBoot states the three impossible shapes.
func TestACoreKindWithoutALocatorIsRefusedAtBoot(t *testing.T) {
	for _, c := range []struct {
		name string
		meta RouteMetadata
		want string
	}{
		{"a kind with no locator", RouteMetadata{CoreKind: CoreKindSession}, "locate the row"},
		{"a kind with both locators",
			RouteMetadata{CoreKind: CoreKindSession, IDParam: "id", BodyIDField: "id"}, "ONE row"},
		{"a locator with no kind", RouteMetadata{IDParam: "id"}, "nothing reads it"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.meta.Validate()
			if err == nil {
				t.Fatalf("%s was accepted: the route would be mounted and deny every caller",
					c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the refusal does not say why (%q): %v", c.want, err)
			}
		})
	}
}

// TestACoreKindRouteCarriesItsLocatorIntoTheEntityRef is the fix itself: the declaration has
// to REACH the engine, not merely exist on the route.
//
// ⛔ IT OBSERVES THE REF AND NOT THE METADATA, because "the field is set on the route" was
// already true of everything the table declared - what was missing is the step that hands it
// over. A test reading the metadata back would have passed before the fix.
func TestACoreKindRouteCarriesItsLocatorIntoTheEntityRef(t *testing.T) {
	got := entityRefFor(RouteMetadata{CoreKind: CoreKindSession, IDParam: "core_session_id"})

	if got == nil {
		t.Fatal("a CoreKind route was mounted with no EntityRef at all")
	}
	if got.CoreKind != CoreKindSession {
		t.Errorf("the ref carries CoreKind %v", got.CoreKind)
	}
	if got.IDParam != "core_session_id" {
		t.Errorf("the ref carries IDParam %q, want core_session_id: the engine has a kind and "+
			"no way to find the row, so it denies before the handler runs", got.IDParam)
	}
}

// TestARouteWithNoCoreKindStillGetsNoRef is the control. Without it the test above passes for
// a registrar that attaches a ref to everything, which would send every self-resolving route
// through a row lookup it does not want.
func TestARouteWithNoCoreKindStillGetsNoRef(t *testing.T) {
	if entityRefFor(RouteMetadata{}) != nil {
		t.Error("a route with no CoreKind was given an EntityRef: it resolves its own resource")
	}
}

// TestAGovernedRouteCarriesItsKindAndConcealIntoTheEntityRef is R-631's transport half.
//
// ⛔ entityRefFor CARRIED THREE OF SEVEN FIELDS, AND THE TWO IT DROPPED MOVE AUTHORIZATION.
// With ResourceKind empty the server DERIVES the kind from the permission — the penultimate
// segment — so a route over a session's transcript authorizes as `transcript`, and the scope
// resolver only re-reads the Session and adds its agent groups when the kind is `session`.
// governance's own test documents that the department forbid reaches kind=session and does not
// reach the derived transcript, which means the policy door could not express the cure that
// test requires. Dropping ConcealDeniedAsNotFound was the other half: a governed listing route
// could not refuse without confirming the row exists.
func TestAGovernedRouteCarriesItsKindAndConcealIntoTheEntityRef(t *testing.T) {
	got := entityRefFor(RouteMetadata{
		CoreKind:                CoreKindSession,
		IDParam:                 "core_session_id",
		ResourceKind:            "session",
		ConcealDeniedAsNotFound: true,
	})
	if got == nil {
		t.Fatal("a route declaring a CoreKind got no EntityRef")
	}
	if got.ResourceKind != "session" {
		t.Errorf("ResourceKind = %q, want \"session\": without it the kind is derived from the "+
			"permission and the scope resolver never adds the session's agent groups",
			got.ResourceKind)
	}
	if !got.ConcealDeniedAsNotFound {
		t.Error("ConcealDeniedAsNotFound was dropped: the route would confirm a row exists by " +
			"refusing differently from a row that does not")
	}
	// The three that already travelled still travel, so this test also fails if the transport
	// is rewritten and loses what it used to carry.
	if got.CoreKind != CoreKindSession || got.IDParam != "core_session_id" {
		t.Errorf("the locator or kind was lost in transport: %+v", got)
	}

	// CONTROL POSITIVE: a route that declares NEITHER new field still gets them zero, which is
	// the derivation it has today. Without this the assertions above would be satisfied by an
	// entityRefFor that hard-coded them.
	plain := entityRefFor(RouteMetadata{CoreKind: CoreKindSession, IDParam: "id"})
	if plain == nil || plain.ResourceKind != "" || plain.ConcealDeniedAsNotFound {
		t.Errorf("a route that declared neither field did not get the historical zero: %+v", plain)
	}
	// And a route with no CoreKind still gets no ref at all.
	if entityRefFor(RouteMetadata{ResourceKind: "session"}) != nil {
		t.Error("a route with no CoreKind was handed an EntityRef: it would be sent through a " +
			"row lookup it does not want and cannot satisfy")
	}
}

// TestTheRegistrationDoorsAreAClosedSet is the guard that keeps R-629 phase 1b from arriving
// with a THIRD loose door.
//
// ⛔ THE POINT IS NOT HOW MANY DOORS THERE ARE, IT IS THAT EACH ONE IS ACCOUNTED FOR. The seal
// only means something while the set of ways to register a route is known: a fourth door added
// later that took a bare RouteMetadata would reopen exactly what SealedRoute closes, and nothing
// would report it — a new method compiles, mounts and serves. So the set is enumerated by
// REFLECTION over the concrete registrar rather than by reading the interface, because
// HandlePolicy is deliberately outside the interface and a future door might be too.
func TestTheRegistrationDoorsAreAClosedSet(t *testing.T) {
	// Each door with the reason it is allowed to exist. A new one fails this test until
	// somebody writes its sentence here, which is the decision being forced.
	want := map[string]string{
		"Handle": "ungoverned: metadata zero on purpose, and R-591 tracks that a governed " +
			"route can still be registered through it by mistake",
		"HandleEntity": "carries the whole EntityRef and forces metadata zero",
		"HandlePolicy": "the LOOSE governed door: accepts a bare RouteMetadata, which is what " +
			"R-629 phase 1b retires or seals once the module that calls it can compile",
		"HandleSealed": "the governed door that only accepts a SealedRoute",
		"HandleNoStore": "the NoStoreRouteRegistrar capability: it does not accept metadata at " +
			"all — it forwards to Handle after attaching response metadata (Cache-Control), so " +
			"it cannot carry an unsealed RouteMetadata past this seal",
		"HandleEntityNoStore": "the same capability for an entity route: it forwards to " +
			"HandleEntity, which already forces metadata zero, and adds only response headers",
	}
	got := map[string]bool{}
	rt := reflect.TypeOf(chiRegistrar{})
	for i := 0; i < rt.NumMethod(); i++ {
		if n := rt.Method(i).Name; strings.HasPrefix(n, "Handle") {
			got[n] = true
		}
	}
	for n := range got {
		if _, ok := want[n]; !ok {
			t.Errorf("a new registration door %q appeared: every door is a way to mount a route, "+
				"and one that takes unsealed metadata undoes SealedRoute. Add it here with the "+
				"reason it may exist, or route it through HandleSealed", n)
		}
	}
	for n := range want {
		if !got[n] {
			t.Errorf("the door %q is gone: if it was retired on purpose, remove it from this map "+
				"in the same change — a map that outlives its subject is what makes the next "+
				"reader trust a set that is no longer the set", n)
		}
	}
}

// TestTheInventoryKeepsThePolicyAndNotJustTheBit is MEDIUM-1 of the fourth contrast: the
// implementation was right and nothing distinguished it from its negation.
//
// ⛔ LA SONDA DEL CONTRASTE ES LA QUE MANDA AQUÍ: quitaron `moduleRoute.meta`, su asignación y
// `recordingRegistrar.HandleSealed` en una copia, y **todo siguió verde**. El caso de la acción
// sólo necesitaba el campo plano `cedarAction`, así que no protegía ni el suelo de AAL, ni el
// grant scoped, ni el rol, ni la herencia, ni el locator, ni `ResourceKind`, ni el conceal — es
// decir, el inventario podía volver a quedarse con el BIT de gobernanza y tirar la política sin
// que nada se pusiera rojo.
//
// ⛔ Y ES UN CASO DEL REGISTRADOR DE INVENTARIO, NO DE `chiRegistrar`. Los tests que mencionaban
// `HandleSealed` inspeccionaban el conjunto de métodos del registrador que MONTA; el que INVENTARÍA
// es otro tipo, y la ruta que se pierde en él desaparece del documento publicado sin que la ruta
// montada cambie. Dos registradores, dos superficies, y la que no se prueba es la que miente.
func TestTheInventoryKeepsThePolicyAndNotJustTheBit(t *testing.T) {
	rich := RouteMetadata{
		RouteMetadata: auth.RouteMetadata{
			CedarAction:                "session:stop",
			RequireScopedGrant:         true,
			RBACMinimumRole:            "admin",
			SessionInheritsAgentGroups: true,
			MinimumAAL:                 3,
		},
		CoreKind:                CoreKindSession,
		IDParam:                 "id",
		ResourceKind:            "session",
		ConcealDeniedAsNotFound: true,
	}

	var routes []moduleRoute
	reg := recordingRegistrar{ns: "inv", out: &routes}
	reg.HandlePolicy("GET", "/a/{id}", "inv:thing:read", rich,
		func(http.ResponseWriter, *http.Request, ModuleContext) {})

	sealed, err := SealRoute(rich)
	if err != nil {
		t.Fatalf("SealRoute: %v", err)
	}
	reg.HandleSealed("GET", "/b/{id}", "inv:thing:read", sealed,
		func(http.ResponseWriter, *http.Request, ModuleContext) {})

	// ⛔ Y UNA TERCERA CON `BodyIDField`, PORQUE NO CABE EN LAS OTRAS DOS. El locator es
	// mutuamente excluyente —una ruta declara `IDParam` o `BodyIDField`, nunca las dos, y el
	// registro la rechaza al montar si trae ambas—, así que un caso que sólo mire la ruta de
	// arriba deja ese campo sin cubrir para siempre. Lo levantó el cuarto contraste, y tenía
	// razón: el inventario podía tirar el locator de cuerpo sin que nada se pusiera rojo.
	body := rich
	body.IDParam = ""
	body.BodyIDField = "session_id"
	reg.HandlePolicy("POST", "/d", "inv:thing:read", body,
		func(http.ResponseWriter, *http.Request, ModuleContext) {})

	// Y la puerta NO gobernada, que es el control positivo: si el inventario guardara la política
	// de todas, este caso pasaría sin que `governed` distinguiera nada.
	reg.Handle("GET", "/c", "inv:thing:read",
		func(http.ResponseWriter, *http.Request, ModuleContext) {})

	if len(routes) != 4 {
		t.Fatalf("el inventario grabó %d rutas, quería 4: sin las cuatro no hay nada que comparar",
			len(routes))
	}
	for i, r := range routes[:2] {
		if !r.governed {
			t.Errorf("ruta %d: el inventario no la marcó gobernada", i)
		}
		// ⛔ CAMPO A CAMPO Y NO `!= rich`: una comparación de struct dice QUE difiere y no CUÁL,
		// y el campo que se pierda mañana es el que nadie nombró hoy.
		if r.meta.CedarAction != rich.CedarAction {
			t.Errorf("ruta %d: perdió la acción", i)
		}
		if r.meta.RequireScopedGrant != rich.RequireScopedGrant {
			t.Errorf("ruta %d: perdió RequireScopedGrant, que RETIRA el término RBAC", i)
		}
		if r.meta.RBACMinimumRole != rich.RBACMinimumRole {
			t.Errorf("ruta %d: perdió el suelo de rol", i)
		}
		if r.meta.SessionInheritsAgentGroups != rich.SessionInheritsAgentGroups {
			t.Errorf("ruta %d: perdió la herencia de grupos del agente", i)
		}
		if r.meta.MinimumAAL != rich.MinimumAAL {
			t.Errorf("ruta %d: perdió el suelo de AAL", i)
		}
		if r.meta.CoreKind != rich.CoreKind || r.meta.IDParam != rich.IDParam {
			t.Errorf("ruta %d: perdió el locator de la fila", i)
		}
		if r.meta.ResourceKind != rich.ResourceKind {
			t.Errorf("ruta %d: perdió ResourceKind, del que depende el resolvedor de ámbito", i)
		}
		if !r.meta.ConcealDeniedAsNotFound {
			t.Errorf("ruta %d: perdió el conceal, que decide si una denegación confirma la fila", i)
		}
	}
	// La tercera lleva el OTRO locator, y es la que el cuarto contraste echó en falta.
	if routes[2].meta.BodyIDField != "session_id" {
		t.Errorf("el inventario perdió BodyIDField (%q): el locator de CUERPO es el que no cabe "+
			"en una ruta con IDParam, así que sin esta ruta nada lo cubría",
			routes[2].meta.BodyIDField)
	}
	if routes[2].meta.IDParam != "" {
		t.Errorf("el inventario inventó un IDParam (%q) en una ruta que declara el locator de "+
			"cuerpo: son mutuamente excluyentes y el registro rechaza las dos juntas",
			routes[2].meta.IDParam)
	}
	if routes[3].governed {
		t.Error("la puerta no gobernada quedó marcada como gobernada: el bit no distingue nada")
	}
	if !routes[3].meta.IsZero() {
		t.Error("la puerta no gobernada grabó política: no declaró ninguna")
	}
}

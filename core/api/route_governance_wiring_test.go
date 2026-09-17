// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// presentEvidenceProducer satisfies the dependency the governed door requires WITHOUT being able
// to install anything, which is exactly the state of the tree today.
//
// ⛔ IT IS NOT A SHORTCUT PAST THE GUARD, and the distinction is the whole point of the guard
// relaxing by PRESENCE. What refuses a boot is nobody having wired a producer at all; what this
// double asserts is that a server WITH one mounts its governed routes. That they then answer 503
// is a different fact, measured below, and it is the fact the HTTP path will keep until principal
// evidence is installed there for real.
type presentEvidenceProducer struct{}

func (presentEvidenceProducer) ResolvePrincipalScope(
	context.Context, auth.PrincipalRef, model.TenantID,
) (auth.Principal, error) {
	return auth.Principal{}, errors.New("test producer installs no evidence")
}

const govWiringPerm auth.Permission = "govwiring:thing:read"

// governanceWiringModule mounts THE SAME HANDLER through both registration doors, which is the
// whole point: any difference the test observes is the door and nothing else.
type governanceWiringModule struct{}

func (*governanceWiringModule) APINamespace() string { return "govwiring" }

func (*governanceWiringModule) Permissions() []auth.Permission {
	return []auth.Permission{govWiringPerm}
}

func (m *governanceWiringModule) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/ungoverned", govWiringPerm, m.handle)

	// The governed door is an optional capability discovered by assertion (server.go), so the
	// module asks for it. A module that finds it absent must NOT fall back to Handle.
	pr, ok := reg.(interface {
		HandlePolicy(method, pattern string, perm auth.Permission, meta api.RouteMetadata, h api.ModuleHandler)
	})
	if !ok {
		panic("govwiring: the registrar does not carry the governed door")
	}

	// ⛔ ZERO METADATA ON PURPOSE, AND ESO ES EL TEST. It is legal metadata, so it is exactly
	// the shape a `meta.IsZero()` split would misroute to the boolean path.
	pr.HandlePolicy("GET", "/governed", govWiringPerm, api.RouteMetadata{}, m.handle)
}

func (m *governanceWiringModule) handle(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"reached":true}`))
}

// TestTheGovernedDoorReachesAuthorizeRouteEvenWithZeroMetadata is the witness for the defect
// this file exists because of, and for the one nobody else would catch.
//
// ⛔ WHAT BROKE: `authzTenantResourcePolicy` was switched to AuthorizeRoute for EVERY route, so
// the fifty-odd ungoverned ones inherited a requirement they never declared — AuthorizeEvidence
// additionally demands a sealed, windowed directory-epoch fact for the principal, and a
// principal without one yields CheckUnknown, which is ErrRouteUndecided, which is 503. Not a
// permission short: THE API DOWN. Fifty tests said so at once.
//
// ⛔ AND THE MUTANT THAT SURVIVES EVERY OTHER TEST IS THE OPPOSITE ONE. Flip HandlePolicy's
// argument to ungovernedRoute and the whole suite stays green — the governed routes would
// quietly serve on the boolean path, minting no witness, and invariant V ("the HTTP effect only
// after a witness") would hold in the comments alone. This case is the only thing that fails.
//
// ⛔ AND IT USES ZERO METADATA DELIBERATELY. The cheap way to write the split is
// `if meta.IsZero()`, which is right for every route in the tree today and wrong by
// construction: it makes emptying a governed route's metadata a SILENT downgrade. With the
// governance carried by the door instead, this route — governed, zero metadata — still reaches
// AuthorizeRoute, and a reader can tell the two designs apart by running this.
func TestTheGovernedDoorReachesAuthorizeRouteEvenWithZeroMetadata(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		// The governed door refuses to mount without a producer, so a test that measures the
		// door has to supply one or it measures the guard instead.
		o.PrincipalEvidenceProducer = presentEvidenceProducer{}
	}, &governanceWiringModule{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "govwiring")
	hdr := map[string]string{"X-Olivares-Tenant": string(tenant)}

	ungoverned := h.do("GET", "/v1/m/govwiring/ungoverned", admin, nil, hdr)
	governed := h.do("GET", "/v1/m/govwiring/governed", admin, nil, hdr)

	// The control positive. Without it, a server that answered 503 to EVERYTHING — a far worse
	// regression — would pass the assertion below without a murmur.
	if ungoverned.code != http.StatusOK {
		t.Fatalf("the UNGOVERNED door did not serve: got %d %s\n"+
			"This route declares no policy, so RBAC and the tenant are the whole question and "+
			"the boolean path answers it. A 503 here is the defect this file documents: the "+
			"evidence path applied to a route that never asked for it.",
			ungoverned.code, ungoverned.raw)
	}

	if governed.code == http.StatusOK {
		t.Fatalf("the GOVERNED door served on the boolean path: got %d %s\n"+
			"A route registered through HandlePolicy must reach AuthorizeRoute and mint a "+
			"witness before any HTTP effect (invariant V). Two ways to get here: HandlePolicy "+
			"now passes ungovernedRoute, or the split was rewritten to key off meta.IsZero() — "+
			"this route's metadata IS zero, which is precisely why it is zero here.",
			governed.code, governed.raw)
	}

	if governed.code != http.StatusServiceUnavailable {
		t.Fatalf("the governed door answered %d %s, expected 503\n"+
			"503 is what AuthorizeRoute returns for THIS harness because its principals carry "+
			"no sealed directory-epoch fact, so the outcome is unknown rather than a denial. If "+
			"harness principals have since gained that evidence, this case has stopped "+
			"discriminating and needs a new way to observe that the governed door was taken — "+
			"it is not, by itself, evidence that the routing is wrong.",
			governed.code, governed.raw)
	}
}

// TestAGovernedRouteWithoutAnEvidenceProducerRefusesToStart is the guard that turns a latent
// outage into a boot failure with the reason in the message.
//
// ⛔ WITHOUT IT THE FAILURE IS A 503 ON EVERY GOVERNED REQUEST, WHICH LOOKS LIKE AN OUTAGE.
// AuthorizeRoute cannot mint a witness for a principal with no sealed evidence, and nothing on the
// HTTP path installs any, so the first governed route the cockpit mounts would serve nothing to
// nobody — and the operator reading the 503s has no way to tell a missing dependency from a broken
// database. Refusing at boot names the precondition.
func TestAGovernedRouteWithoutAnEvidenceProducerRefusesToStart(t *testing.T) {
	// Everything a server needs EXCEPT the producer, built by the same function the harness uses
	// — so this case cannot pass because the composition was wrong in some other way.
	opts, _, _, _, _, _ := newHarnessOptions(t, &governanceWiringModule{})
	_, err := api.New(opts)
	if err == nil {
		t.Fatal("a server mounting a governed route started with no evidence producer: every " +
			"request to that route would answer 503 and nothing would say why")
	}
	// ⛔ THE MESSAGE IS THE REMEDY, so it is asserted rather than trusted. An error that only
	// says "refused" leaves the reader exactly where the 503 would have.
	for _, want := range []string{"governed route", "PrincipalEvidenceProducer", "GET /governed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q, so it does not say what to install: %v", want, err)
		}
	}
}

// TestAnUngovernedServerStartsWithoutAnEvidenceProducer is the control positive, and without it
// the case above passes on an api.New that refuses everything.
//
// It is also the assertion that today's tree boots: no module registers a governed route, so the
// guard is inert for the real composition root. The day one does, the case above fires first.
func TestAnUngovernedServerStartsWithoutAnEvidenceProducer(t *testing.T) {
	h := newHarness(t)
	if h.srv == nil {
		t.Fatal("the default harness did not build a server")
	}
}

const concealPerm auth.Permission = "govconceal:session:read"

// forbiddingPolicy implements BOTH PolicyEvaluator (what NewAuthorizer takes) and
// PolicyEvidenceEvaluator (what the evidence path asserts for).
//
// ⛔ IMPLEMENTING ONLY THE FIRST WOULD GUT EVERY CASE BELOW. A plain PolicyEvaluator is never
// consulted by AuthorizeEvidence: it lands on "policy_evidence_legacy" -> CheckUnknown -> 503, so
// the deny cases would see the THIRD STATE, agree with themselves, and verify nothing about
// denial at all. CheckBroken is what short-circuits combineEvidenceOutcome to EvidenceDeny and
// wins over the CheckUnknown that the missing principal evidence contributes — which is what makes
// a policy denial observable over HTTP while nothing installs that evidence.
type forbiddingPolicy struct{}

// ⛔ FORBIDS ONLY THIS MODULE'S PERMISSION, AND THE SCOPE IS NOT TIDINESS. A policy that
// forbade everything also forbade the harness's own setup — creating the org came back 403 and
// the case died before reaching the route it was written for. A fixture that denies globally does
// not test a denial: it tests that nothing works, which passes for every reason including the
// wrong ones.
func (forbiddingPolicy) Evaluate(_ context.Context, req auth.Request) (auth.Decision, error) {
	return auth.Decision{Allow: req.Permission != concealPerm}, nil
}

func (forbiddingPolicy) EvaluateEvidence(
	_ context.Context, req auth.Request,
) (auth.PolicyEvidenceDecision, error) {
	if req.Permission != concealPerm {
		return auth.PolicyEvidenceDecision{
			ForbidAbsence: auth.CheckEvidence{
				Verdict: auth.CheckClean, Code: "policy_forbid_not_matched",
			},
		}, nil
	}
	now := time.Now()
	return auth.PolicyEvidenceDecision{
		ForbidAbsence: auth.CheckEvidence{Verdict: auth.CheckBroken, Code: "policy_forbid_matched"},
		ObservedAt:    now.Add(-time.Minute),
		FreshUntil:    now.Add(time.Hour),
	}, nil
}

// concealResolver answers for exactly one row, so a case can ask about a row that EXISTS and one
// that does not — the pair the conceal oracle is about — and it COUNTS.
//
// ⛔ EL CONTADOR ES LA MITAD QUE FALTABA. La versión anterior sabía si la fila existía y no cuántas
// veces se la habían pedido, así que podía afirmar «las cuatro respuestas son idénticas» y no
// «no se leyó ninguna fila» — que es lo que el contrato promete. Y era falso: el cargador corría
// ANTES del 503. Cuatro cuerpos iguales con cuatro lecturas hechas no cumplen la promesa; la
// igualdad del cuerpo no dice nada del coste, la latencia, los bloqueos ni las trazas, que son un
// oráculo por otra vía.
type concealResolver struct {
	present model.ID
	reads   atomic.Int64
}

func (r *concealResolver) ResolveCoreEntity(
	_ context.Context, tenant model.TenantID, _ api.CoreKind, id model.ID,
) (api.CoreEntityFacts, error) {
	r.reads.Add(1)
	return api.CoreEntityFacts{ID: id, Tenant: tenant, Exists: id == r.present}, nil
}

// concealModule mounts the SAME handler on a concealing and a non-concealing governed route, plus
// one ungoverned route as the reachability control.
type concealModule struct{ ran atomic.Int64 }

func (*concealModule) APINamespace() string { return "govconceal" }

func (*concealModule) Permissions() []auth.Permission {
	return []auth.Permission{concealPerm}
}

func (m *concealModule) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/reachable", concealPerm, m.handle)
	pr, ok := reg.(interface {
		HandlePolicy(method, pattern string, perm auth.Permission, meta api.RouteMetadata, h api.ModuleHandler)
	})
	if !ok {
		panic("govconceal: the registrar does not carry the governed door")
	}
	// ⛔ POR QUÉ EL CONCEAL SE MIDE EN LA PUERTA NO GOBERNADA, Y NO ES DONDE YO QUERÍA MEDIRLO.
	// La puerta de disponibilidad que exige el tercer estado ANTES del cargador se pregunta lo
	// único que no depende del recurso: si la autoridad del principal se puede reconstruir. Con
	// evidencia ausente —y hoy NINGÚN principal del camino HTTP la tiene— responde 503 antes de
	// consultar la política, así que **una denegación de política deja de ser observable en una
	// ruta gobernada**. Ése es el precio que la puerta declara, no un efecto colateral.
	//
	// ⇒ El conceal-404 se sigue midiendo aquí, en la puerta que hoy puede llegar a denegar; y su
	// gemelo gobernado —misma promesa, otra puerta— sólo será medible cuando el camino de petición
	// instale evidencia. Escribirlo hoy daría 503 en las dos mitades y **pasaría en verde
	// afirmando lo contrario de su nombre**, que es la trampa de este fichero entero.
	entity := api.EntityRef{CoreKind: api.CoreKindSession, IDParam: "id"}
	concealing := entity
	concealing.ConcealDeniedAsNotFound = true
	reg.HandleEntity("GET", "/conceal/{id}", concealPerm, concealing, m.handle)
	reg.HandleEntity("GET", "/plain/{id}", concealPerm, entity, m.handle)

	// ⛔ Y LAS MISMAS DOS POR LA PUERTA GOBERNADA, PORQUE LAS DOS PREGUNTAS VIVEN EN PUERTAS
	// DISTINTAS Y NO SE PUEDEN MEDIR EN LA MISMA RUTA. El conceal-404 exige una DENEGACIÓN, y en
	// una ruta gobernada la puerta de disponibilidad responde 503 antes de consultar la política
	// mientras nada instale evidencia. El tercer estado exige justo eso: una ruta gobernada. Una
	// sola pareja de rutas sólo puede medir una de las dos, y la que se quede sin medir sería la
	// que pasara en verde sin ejercitar nada.
	gm := api.RouteMetadata{
		RouteMetadata: auth.RouteMetadata{CedarAction: "session:read"},
		CoreKind:      api.CoreKindSession, IDParam: "id",
	}
	gConceal := gm
	gConceal.ConcealDeniedAsNotFound = true
	pr.HandlePolicy("GET", "/g-conceal/{id}", concealPerm, gConceal, m.handle)
	pr.HandlePolicy("GET", "/g-plain/{id}", concealPerm, gm, m.handle)
}

func (m *concealModule) handle(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	m.ran.Add(1)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"reached":true}`))
}

// newConcealHarness mounts concealModule with a chosen authorizer. present is the row id that
// exists; every other id does not.
func newConcealHarness(
	t *testing.T, az *auth.Authorizer, res *concealResolver, m *concealModule,
) *harness {
	t.Helper()
	return newHarnessOpts(t, func(o *api.Options) {
		o.Authorizer = az
		o.CoreEntityResolver = res
		o.PrincipalEvidenceProducer = presentEvidenceProducer{}
	}, m)
}

// TestAConcealingRouteStillAnswers404WhenPolicyDenies decides whether R-614 opened an existence
// oracle while closing the witness gap.
//
// ⛔ THE DANGEROUS DIRECTION IS THE ONE THAT LOOKS LIKE PROGRESS. AuthorizeRoute returns typed
// errors, and writing them instead of the denial the ROUTE chose reads as an improvement: richer
// errors, less indirection. It would also turn every conceal-404 into a 403, and a 403 where a 404
// used to be says "the row is there, you may not have it" — the whole of what
// ConcealDeniedAsNotFound exists to prevent.
func TestAConcealingRouteStillAnswers404WhenPolicyDenies(t *testing.T) {
	row := model.NewID()
	m := &concealModule{}
	res := &concealResolver{present: row}
	h := newConcealHarness(t, auth.NewAuthorizer(forbiddingPolicy{}), res, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "govconceal")
	hdr := tenantHdr(tenant)

	concealed := h.do("GET", "/v1/m/govconceal/conceal/"+row.String(), admin, nil, hdr)
	if concealed.code != http.StatusNotFound {
		t.Errorf("a concealing route denied by policy answered %d, want 404: a 403 here confirms "+
			"the row exists, which is the one thing conceal exists to prevent (%s)",
			concealed.code, concealed.raw)
	}

	// ⛔ THE CONTROL POSITIVE. Without it, an implementation that answered 404 to EVERYTHING — a
	// worse regression, because it hides denials from their own operators — passes the assertion
	// above without a murmur.
	plain := h.do("GET", "/v1/m/govconceal/plain/"+row.String(), admin, nil, hdr)
	if plain.code != http.StatusForbidden {
		t.Errorf("a NON-concealing route denied by the same policy answered %d, want 403: if this "+
			"is also 404 then the case above proves nothing about conceal (%s)",
			plain.code, plain.raw)
	}

	if m.ran.Load() != 0 {
		t.Errorf("the handler ran %d time(s) on a denied route: the HTTP effect happened without "+
			"an authorization that allowed it", m.ran.Load())
	}
}

// TestUndecidedAnswersIdenticallyWithAndWithoutConceal is the half almost nobody writes.
//
// ⛔ THE THIRD STATE CAN LEAK TOO. If a route that hides existence answered "I could not decide"
// differently from one that does not, that undecided would be an oracle by itself: provoke it and
// compare. The four answers — conceal x {row present, row absent} — must have ONE shape, and it is
// produced BEFORE any row is read, which is why the resolver's answer cannot influence it.
func TestUndecidedAnswersIdenticallyWithAndWithoutConceal(t *testing.T) {
	present := model.NewID()
	absent := model.NewID()
	m := &concealModule{}
	res := &concealResolver{present: present}
	// The default authorizer: no policy denies, and no principal on the HTTP path carries the
	// sealed evidence AuthorizeEvidence needs, so the outcome is UNKNOWN for all four.
	h := newConcealHarness(t, auth.NewAuthorizer(nil), res, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "govconceal")
	hdr := tenantHdr(tenant)

	type answer struct {
		name string
		r    resp
	}
	var got []answer
	for _, route := range []string{"conceal", "plain"} {
		for _, id := range []struct {
			what string
			id   model.ID
		}{{"row present", present}, {"row absent", absent}} {
			got = append(got, answer{
				name: route + ", " + id.what,
				r:    h.do("GET", "/v1/m/govconceal/g-"+route+"/"+id.id.String(), admin, nil, hdr),
			})
		}
	}

	first := got[0]
	if first.r.code != http.StatusServiceUnavailable {
		t.Fatalf("%s answered %d, want 503: this case can only compare shapes if the third state "+
			"is what it is comparing (%s)", first.name, first.r.code, first.r.raw)
	}
	if first.r.hdr.Get("Retry-After") == "" {
		t.Error("the third state carries no Retry-After: a client cannot tell it from a refusal, " +
			"and the whole point is that nothing said no")
	}
	for _, a := range got[1:] {
		if a.r.code != first.r.code || a.r.raw != first.r.raw ||
			a.r.hdr.Get("Retry-After") != first.r.hdr.Get("Retry-After") {
			t.Errorf("%s answered %d %q (Retry-After %q) but %s answered %d %q (Retry-After %q): "+
				"an undecided that differs by conceal or by whether the row exists IS the oracle "+
				"that conceal exists to close",
				a.name, a.r.code, a.r.raw, a.r.hdr.Get("Retry-After"),
				first.name, first.r.code, first.r.raw, first.r.hdr.Get("Retry-After"))
		}
	}

	// ⛔ Y NINGUNA FILA SE LEYÓ, que es la mitad que el contrato promete y la fixture anterior no
	// podía afirmar. Un mutante que mueva UNA sola llamada al resolvedor delante del 503 pone esto
	// en rojo, aunque las cuatro respuestas sigan siendo idénticas byte a byte.
	if n := res.reads.Load(); n != 0 {
		t.Errorf("el resolvedor de entidad se llamó %d vez/veces antes del tercer estado: el 503 "+
			"promete no haber leído ninguna fila, y coste, latencia, bloqueos y trazas del "+
			"resolvedor son un oráculo aunque el cuerpo salga igual", n)
	}

	// ⛔ AND THE HANDLER NEVER RAN, which is invariant V in the direction that matters: no witness
	// could be minted, so no HTTP effect happened. The mutant is a door that runs the handler and
	// lets it decide — the shape this whole file exists to make impossible.
	if m.ran.Load() != 0 {
		t.Errorf("the handler ran %d time(s) with no witness: the governed door let the effect "+
			"happen before an authorization that could allow it", m.ran.Load())
	}

	// CONTROL POSITIVO DE ALCANZABILIDAD: sin él, un servidor que no llamara a NINGÚN handler
	// satisface la aserción de arriba y este fichero entero pasa midiendo nada.
	reachable := h.do("GET", "/v1/m/govconceal/reachable", admin, nil, hdr)
	if reachable.code != http.StatusOK || m.ran.Load() != 1 {
		t.Fatalf("the ungoverned route answered %d and ran the handler %d time(s), want 200 and 1: "+
			"if the handler is unreachable by ANY door, 'it did not run' above says nothing",
			reachable.code, m.ran.Load())
	}
}

// Actions is what lets concealModule's routes name session:read. Without it the mount panics,
// which is the guard below working rather than a fixture detail.
func (*concealModule) Actions() []auth.CedarAction { return []auth.CedarAction{"session:read"} }

// undeclaredActionModule names an action it never declares. It declares ANOTHER one, so the case
// cannot pass merely because the module registered nothing.
type undeclaredActionModule struct{ declare []auth.CedarAction }

func (*undeclaredActionModule) APINamespace() string { return "govundeclared" }

func (*undeclaredActionModule) Permissions() []auth.Permission {
	return []auth.Permission{"govundeclared:thing:read"}
}

func (m *undeclaredActionModule) Actions() []auth.CedarAction { return m.declare }

func (m *undeclaredActionModule) APIRoutes(reg api.RouteRegistrar) {
	pr, ok := reg.(interface {
		HandlePolicy(method, pattern string, perm auth.Permission, meta api.RouteMetadata, h api.ModuleHandler)
	})
	if !ok {
		panic("govundeclared: the registrar does not carry the governed door")
	}
	pr.HandlePolicy("GET", "/x", "govundeclared:thing:read",
		api.RouteMetadata{RouteMetadata: auth.RouteMetadata{CedarAction: "borrowed:read"}},
		func(http.ResponseWriter, *http.Request, api.ModuleContext) {})
}

// lenderModule declares the action the module above borrows, and mounts nothing.
type lenderModule struct{}

func (*lenderModule) APINamespace() string           { return "govlender" }
func (*lenderModule) Permissions() []auth.Permission { return nil }
func (*lenderModule) APIRoutes(api.RouteRegistrar)   {}
func (*lenderModule) Actions() []auth.CedarAction    { return []auth.CedarAction{"borrowed:read"} }

// mountErr returns the error from building a server with these modules.
//
// ⛔ ERROR Y NO PANIC, Y EL CAMBIO CORRIGE UNA AFIRMACIÓN MÍA QUE ERA FALSA. Yo escribí que
// «api.New devuelve error nombrando la regla» mientras la validación vivía en HandlePolicy y hacía
// panic; este ayudante tenía que convertirlo en dato con `recover`, y esa conversión es la huella
// de una afirmación que no encaja con su código. La comprobación vive ahora en el arranque, que es
// donde ya viven sus vecinas, y devuelve error: la afirmación y el código dicen lo mismo.
func mountErr(t *testing.T, modules ...api.Module) error {
	t.Helper()
	opts, _, _, _, _, _ := newHarnessOptions(t, modules...)
	opts.PrincipalEvidenceProducer = presentEvidenceProducer{}
	_, err := api.New(opts)
	return err
}

// TestARouteMayOnlyNameAnActionItsOwnModuleDeclares is the catalogue measured in the one
// direction that distinguishes OWNERSHIP from ORDER.
//
// ⛔ THE FIRST TWO CASES ALONE WOULD PASS ON A GLOBAL CATALOGUE. "declares and mounts" and "does
// not declare and is refused" are both satisfied by a flat set of every action any module
// registered — as long as the modules happen to mount in a lucky order. The difference only shows
// the day someone reorders them, which is the worst day to find out.
//
// So the third case borrows: the action IS declared, by ANOTHER module, mounted BEFORE this one,
// so a global catalogue would have it in hand and let the route through. It must be refused
// anyway, because what the rule says is "its own" and not "known to somebody".
func TestARouteMayOnlyNameAnActionItsOwnModuleDeclares(t *testing.T) {
	// Declares what it names: mounts.
	if err := mountErr(t, &undeclaredActionModule{declare: []auth.CedarAction{"borrowed:read"}}); err != nil {
		t.Fatalf("a module naming an action it DECLARES was refused: %v", err)
	}

	// Names one it does not declare: refused, and the message has to say the rule.
	err := mountErr(t, &undeclaredActionModule{declare: []auth.CedarAction{"unrelated:read"}})
	if err == nil {
		t.Fatal("a route named a Cedar action its module never declared and mounted anyway: the " +
			"action reaches the policy engine as an entity nobody registered")
	}
	for _, want := range []string{"borrowed:read", "govundeclared", "declares itself"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q, so it does not say what to do: %v", want, err)
		}
	}

	// ⛔ THE CASE THAT DECIDES WHAT WE BUILT: declared by another module, mounted FIRST.
	err = mountErr(t, &lenderModule{}, &undeclaredActionModule{declare: []auth.CedarAction{"unrelated:read"}})
	if err == nil {
		t.Fatal("a route mounted while naming an action ANOTHER module declared: the catalogue is " +
			"global, so this route mounts today and stops mounting the day that module is not " +
			"loaded or the two mount in the other order — a dependency nothing declares")
	}

	// ⛔⛔ Y ESTA ES LA ASERCIÓN QUE FALTABA, Y SIN ELLA EL CASO DE ARRIBA PASABA POR LA RAZÓN
	// EQUIVOCADA. `RegisterModulePermissions` construía su snapshot sin copiar las acciones, así
	// que el registro de permisos del PRESTATARIO borraba las del PRESTAMISTA antes de que nadie
	// comprobara nada: la ruta se rechazaba por PÉRDIDA DE ESTADO y no por PERTENENCIA, que es
	// justo la distinción que este caso existe para hacer. Exigir que la acción del prestamista
	// SIGA declarada por él en el momento del rechazo es lo que separa las dos causas — y lo que
	// pone en rojo cualquier regresión que vuelva a tirar el catálogo.
	if !auth.ModuleDeclaresAction("govlender", "borrowed:read") {
		t.Fatal("el prestamista dejó de declarar su propia acción: el rechazo de arriba fue por " +
			"pérdida de estado del catálogo, no por pertenencia, y este caso no mide lo que dice")
	}
	if auth.ModuleDeclaresAction("govundeclared", "borrowed:read") {
		t.Fatal("el prestatario aparece declarando una acción que nunca declaró")
	}
}

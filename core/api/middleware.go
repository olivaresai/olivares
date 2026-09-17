// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// statusRecorder captures the response status for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap exposes the wrapped ResponseWriter so http.ResponseController can reach
// the base writer's optional capabilities (Flush, SetWriteDeadline, Hijack). A
// streaming module route — server-sent events for live operation (module II),
// later voice/realtime (module XVI) — needs to flush each event and clear the
// hardened WriteTimeout for the duration of the stream; without this Unwrap the
// access-log wrapper would hide those capabilities. It does not change the
// status capture for any non-streaming response.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// recoverer turns a handler panic into a 500 (and logs it) so one bad handler
// cannot crash the server. It composes with — does not replace — the eventbus /
// runtime panic isolation (S02), which guards background work.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("api: handler panic", "panic", rec, "path", r.URL.Path, "request_id", requestID(r.Context()))
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"internal error"}}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestIDMW assigns or propagates a request id, exposed as X-Request-ID.
func (s *Server) requestIDMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(withRequestID(r.Context(), id)))
	})
}

// secureHeaders sets conservative security headers on every response.
func (s *Server) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		// HSTS is safe because the server is TLS-by-default.
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// accessLog logs one line per request AFTER it completes. It never logs the
// request body or the Authorization header (so a token or setup token cannot leak
// into logs). It installs an actor holder that the authenticate middleware (which
// runs inside) fills, so the line is attributed to the real principal.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.clock.Now().Time()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		ctx, holder := withActorHolder(r.Context())
		s.mInflight.Inc()
		next.ServeHTTP(rec, r.WithContext(ctx))
		dur := time.Since(start)
		s.mInflight.Dec()
		s.recordRequest(r.Method, rec.status, dur)
		s.log.Info("api request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"dur_ms", dur.Milliseconds(), "actor", holder.actor,
			"request_id", requestID(r.Context()))
	})
}

// authenticate resolves a bearer credential into the request context. A present
// but invalid credential is rejected immediately (401); an absent credential
// leaves the request anonymous for routes that allow it (login, setup, health).
//
// when a metricsGate is configured, /metrics handles its OWN bearer auth
// (a static scrape token, not an API credential) — the middleware passes through
// so the endpoint's allowMetrics gate resolves it. Without a gate the original
// behavior is unchanged (the scraper presents no bearer and passes through as
// anonymous).
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if h == "" {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/metrics" && s.metricsGate != nil {
			next.ServeHTTP(w, r)
			return
		}
		token, ok := strings.CutPrefix(h, "Bearer ")
		if !ok {
			s.writeError(w, r, auth.ErrUnauthenticated)
			return
		}
		p, err := s.authr.Authenticate(r.Context(), strings.TrimSpace(token))
		if err != nil {
			s.writeError(w, r, auth.ErrUnauthenticated)
			return
		}
		if h := actorHolderFrom(r.Context()); h != nil {
			h.actor = p.Actor() // attribute the access-log line to the real principal
		}
		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

// RootEnginePaths are the engine's root-level, non-/v1, unauthenticated endpoints:
// the health check, the OpenAPI document, the Prometheus scrape target and the
// liveness/readiness probes. They are the SINGLE SOURCE shared by the setup-gate
// exemption (below) and the SPA-vs-API router in the single-binary composition root
// (cmd/olivares's isAPIPath), so the two lists can never drift apart. That drift
// once shadowed /livez, /readyz and /metrics behind the SPA shell — they returned
// index.html 200 instead of the engine handlers, so the Helm readinessProbe never
// saw the store-down 503 and the LB kept routing to wedged pods (C1).
var RootEnginePaths = []string{"/healthz", "/openapi.json", "/openapi.beta.json", "/metrics", "/livez", "/readyz", "/pod-readyz", "/status", AuthZenConfigPath, OAuthAuthorizationServerMetadataPath}

// leaderGate is the HA leader-routing backstop (stage-2, design §B.1). In the
// leader-routing layout every healthy replica is Pod-Ready, so a standby IS
// reachable: the leader-selecting Service resolves traffic from a label the engine
// publishes, and a label is not transactional with the Postgres election lock. A
// brief stale label — or a direct dial to a pod IP — could otherwise land an
// application request on a standby, which would serve READS from a node that is
// not the active writer (the store's write fence stops writes, not reads).
//
// So every application route re-checks leadership at the edge and answers the same
// retryable 503 not_leader the write fence produces. Labels affect discovery;
// leadership authority stays with the Postgres lock. The operational endpoints
// (probes, metrics, status, discovery documents) are exempt — the kubelet and the
// scraper MUST reach a standby, and /readyz's own leader verdict is precisely the
// signal a failover test reads.
//
// It is opt-in (Options.LeaderRouteGate): only pods deployed in the leader-routing
// layout enable it, so a legacy HA deployment — where standbys are drained from
// the Service by /readyz and reads to them are deliberate — is unchanged.
//
// The predicate is IsLeader(), NOT Active(). The store-private bootstrap gate can
// remain open during PROMOTION, but public Active and IsLeader stay false until
// the callback establishes leadership. IsLeader() is the signal the label
// publisher advertises on, so the gate and the routing label can never disagree.
// prepareGovernedPrincipal installs the principal's SEALED authority evidence for a governed
// route, and it is the only place in this package that calls the evidence producer.
//
// ⛔ ÉSTE ERA EL HUECO, Y LO ENCONTRÓ UN TESTIGO, NO UNA LECTURA. `PrincipalEvidenceProducer`
// estaba declarado, cableado en boot y comprobado AL MONTAR (checkGovernedRoutes), y su método no
// lo invocaba nadie: el principal de la petición nunca recibía evidencia, de modo que
// PrincipalAuthorityAvailable fallaba SIEMPRE y toda ruta gobernada respondía 503. Un cableado sin
// llamada es una declaración, y sólo un caso que EJERCITA el camino de éxito puede distinguirlos.
//
// ⛔ Y EL PRINCIPAL SE SUSTITUYE EN EL CONTEXTO ORIGINAL, NO EN EL HIJO. El hijo lleva el plazo de
// cinco segundos que acota ESTA lectura y se cancela al volver; propagarlo cancelaría el resto de
// la petición en cuanto venciera. Son dos duraciones distintas y la del productor no es la del
// cliente. El `Retry-After` tampoco es ese plazo: uno acota la lectura, el otro orienta el reintento.
//
// El orden de los estados es el de K3-EVIDENCIA-HTTP-SPEC §2.3, no una elección de aquí: 401 sin
// principal y ante una credencial concluyentemente inválida; el 400/403 del tenant se conserva; y
// todo lo demás colapsa a un 503 UNIFORME cuya causa va al log y nunca al cuerpo, porque distinguir
// «sin referencia» de «sin productor» de «evidencia inservible» es un oráculo sobre el estado
// interno de la autoridad.
func (s *Server) prepareGovernedPrincipal(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	raw, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return r, false
	}
	tenant, err := s.resolveTenant(r, raw)
	if err != nil {
		// Un tenant inválido o que contradice el binding del token NO es «no pude decidir»:
		// conserva el 400/403 que ya daba, y el productor ni se llama.
		s.writeError(w, r, err)
		return r, false
	}

	// Declarada ANTES de la clausura que la lee: `undecided` se define aquí arriba y la medida se
	// toma más abajo, así que un `:=` en el punto de la medida la dejaría fuera de su alcance.
	var espera time.Duration
	undecided := func(why string, cause error) (*http.Request, bool) {
		if s.log != nil {
			s.log.Info("api: the principal's authority could not be reconstructed",
				"why", why, "err", cause, "path", r.URL.Path, "producer_ms", espera.Milliseconds())
		}
		w.Header().Set("Retry-After", "5")
		s.writeError(w, r, auth.ErrRouteUndecided)
		return r, false
	}

	ref, ok := raw.Ref()
	if !ok {
		return undecided("the principal carries no credential reference", nil)
	}
	if s.principalEvidence == nil {
		return undecided("no evidence producer is installed", nil)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	// El coste de la reconstrucción se MIDE, no se supone: es una lectura de store por petición
	// gobernada, y una sola cifra en el log distingue «el productor es caro» de «lo caro está en
	// otro sitio». La primera corrida de esta puerta hizo sospechar del productor y los tiempos
	// decían otra cosa — el caso cuyo doble falla al instante tardaba lo mismo.
	inicio := time.Now()
	resolved, perr := s.principalEvidence.ResolvePrincipalScope(ctx, ref, tenant)
	espera = time.Since(inicio)
	if perr != nil {
		// Una credencial concluyentemente inválida se responde 401 y no 503: son dos remedios
		// distintos, y servir «reintenta» a quien tiene que volver a autenticarse es una espera
		// que nunca termina.
		if errors.Is(perr, auth.ErrUnauthenticated) {
			s.writeError(w, r, auth.ErrUnauthenticated)
			return r, false
		}
		return undecided("the producer could not reconstruct the scope", perr)
	}
	if got, ok := resolved.Ref(); !ok || got != ref {
		// La referencia de salida tiene que ser la de entrada: un productor que devolviera otra
		// habría cambiado de sujeto a mitad de la decisión.
		return undecided("the reconstructed principal does not carry the same reference", nil)
	}
	if aerr := s.authz.PrincipalAuthorityAvailable(resolved, tenant); aerr != nil {
		return undecided("the reconstructed authority is not usable for this tenant", aerr)
	}

	if h := actorHolderFrom(r.Context()); h != nil {
		h.actor = resolved.Actor()
	}
	return r.WithContext(withPrincipal(r.Context(), resolved)), true
}

func (s *Server) leaderGate(next http.Handler) http.Handler {
	exempt := make(map[string]bool, len(RootEnginePaths))
	for _, p := range RootEnginePaths {
		exempt[p] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt[r.URL.Path] || s.st.Leader().IsLeader() {
			next.ServeHTTP(w, r)
			return
		}
		s.writeError(w, r, store.ErrNotLeader)
	})
}

// setupGate blocks every non-exempt route with 409 setup_required until the first
// superadmin exists. The check is transactional (HasAnyUser) until setup is first
// observed complete, then cached — so a crash mid-setup safely re-enters setup
// mode, while steady state costs no extra query.
func (s *Server) setupGate(next http.Handler) http.Handler {
	// The exempt set is the shared root endpoints PLUS the two unauthenticated /v1
	// leaves needed to bootstrap (server-info and first-run setup).
	exempt := map[string]bool{"/v1/server-info": true, "/v1/setup": true}
	for _, p := range RootEnginePaths {
		exempt[p] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt[r.URL.Path] || s.setupCompleteNow(r) {
			next.ServeHTTP(w, r)
			return
		}
		s.writeError(w, r, errSetupRequired)
	})
}

// setupCompleteNow reports whether setup is complete, caching the first true.
func (s *Server) setupCompleteNow(r *http.Request) bool {
	return s.isSetupComplete(r.Context())
}

// isSetupComplete uses the shared setup-state observation and fails closed on error.
// The REST setup gate and gRPC path retain their boolean authorization contract.
func (s *Server) isSetupComplete(ctx context.Context) bool {
	complete, err := s.setupState(ctx)
	if err != nil {
		s.log.Error("api: setup-gate check failed", "err", err)
		return false // fail closed: stay in setup mode on error
	}
	return complete
}

// setupState queries the global authentication authority and caches the first true.
// Errors and negative observations are not cached. Readiness uses the error to
// distinguish an unknown state from an installation with no users.
func (s *Server) setupState(ctx context.Context) (bool, error) {
	if s.setupComplete.Load() {
		return true, nil
	}
	has, err := s.authr.HasAnyUser(ctx)
	if err != nil {
		return false, err
	}
	if has {
		s.setupComplete.Store(true)
	}
	return has, nil
}

// authzTenant authenticates, resolves the single canonical tenant, and authorizes
// perm at COLLECTION level (no specific entity). On any failure it writes the response
// and returns ok=false. The returned tenant is the ONLY tenant a handler may use (never
// re-derive from the request).
func (s *Server) authzTenant(w http.ResponseWriter, r *http.Request, perm auth.Permission) (auth.Principal, model.TenantID, bool) {
	return s.authzTenantResource(w, r, perm, auth.ResourceFor(perm))
}

// authzTenantEntity authorizes perm for a SPECIFIC entity: it seeds Resource.ID
// so the scoped-grant engine resolves the entity's TRUE scope (its workspace,
// folder ancestors and agent-group membership) from the stored row — which the caller
// cannot forge — and may GRANT a workspace-scoped principal access the flat RBAC layer
// would deny, or a forbid may narrow it. When no scoped grants are active the engine
// abstains and this is exactly authzTenant. An empty id degrades safely to collection
// level. Same failure/return contract as authzTenant.
func (s *Server) authzTenantEntity(w http.ResponseWriter, r *http.Request, perm auth.Permission, id model.ID) (auth.Principal, model.TenantID, bool) {
	res := auth.ResourceFor(perm)
	res.ID = id.String()
	return s.authzTenantResource(w, r, perm, res)
}

// authzTenantEntityKind authorizes an entity action whose entity KIND differs from the
// permission's resource segment — e.g. an agent-group read gated by "agent:read" but whose
// entity is an "agent_group". The kind drives the scoped engine's workspace resolution
// (readScope, F3), so it MUST name the STORED entity, not the permission's resource;
// otherwise the engine cannot derive the group's workspace and a workspace-confined operator
// reads a cross-workspace group.
func (s *Server) authzTenantEntityKind(w http.ResponseWriter, r *http.Request, perm auth.Permission, kind string, id model.ID) (auth.Principal, model.TenantID, bool) {
	return s.authzTenantResource(w, r, perm, auth.ResourceAttrs{Kind: kind, ID: id.String()})
}

// authzTenantResource is the shared core of authzTenant/authzTenantEntity: it
// authenticates, resolves the single canonical tenant, and authorizes perm against the
// given resource attributes.
func (s *Server) authzTenantResource(w http.ResponseWriter, r *http.Request, perm auth.Permission, res auth.ResourceAttrs) (auth.Principal, model.TenantID, bool) {
	return s.authzTenantResourceWithDenial(w, r, perm, res, errForbidden)
}

// authzTenantResourceWithDenial keeps authentication, tenant resolution and the
// authorization decision in the one shared flow while allowing an exact point
// route to conceal only its final denial. Callers must pass a curated API error;
// authentication and tenant failures are always written before it is considered.
func (s *Server) authzTenantResourceWithDenial(
	w http.ResponseWriter,
	r *http.Request,
	perm auth.Permission,
	res auth.ResourceAttrs,
	denial error,
) (auth.Principal, model.TenantID, bool) {
	// Las puertas no gobernadas descartan el testigo: su metadata es cero, asi que no hay
	// pregunta sellada que conservar. El campo queda a cero en su ModuleContext.
	p, tenant, _, ok := s.authzTenantResourcePolicy(
		w, r, perm, res, auth.RouteMetadata{}, denial, ungovernedRoute,
	)
	return p, tenant, ok
}

// routeGovernance says WHICH DOOR a request arrived through. It is a named type and not a bare
// bool so a call site cannot pass the wrong one by position and still compile, and it is a
// PARAMETER and not a property of the metadata so that emptying the metadata cannot downgrade a
// governed route to the boolean path in silence.
type routeGovernance bool

const (
	// ungovernedRoute declared no policy: RBAC and the tenant are the whole question, and the
	// witness stays zero, which authorizes nothing by construction.
	ungovernedRoute routeGovernance = false
	// governedRoute was registered through HandlePolicy/HandleSealed and must produce a witness
	// before any HTTP effect (invariant V).
	governedRoute routeGovernance = true
)

// authzTenantResourcePolicy is the same flow with the route's SEALED metadata carried into
// the decision (V269 / architecture §7.1).
//
// ⛔ AQUI DECIA «THE ZERO METADATA IS BIT-FOR-BIT THE OLD BEHAVIOUR». ERA CIERTO Y DEJO DE
// SERLO EN EL MISMO COMMIT QUE ESTE COMENTARIO SOBREVIVIO, y la suite lo desmintio con
// CINCUENTA tests en rojo sirviendo 503 en rutas ordinarias — crear un agente, listar,
// auditar.
//
// Lo que la frase afirmaba de la METADATA sigue siendo verdad: cada arma de rbacPermitted
// solo RETIRA el termino RBAC, y el resolvedor consulta SessionInheritsAgentGroups solo
// cuando es true, asi que el cero es inerte. Pero la equivalencia no vivia ahi. Vivia en que
// las dos puertas llamaban a la MISMA funcion, y al cambiarla a AuthorizeRoute la divergencia
// se mudo de eje: `Authorize` pregunta «¿RBAC o grant permiten?», y `AuthorizeEvidence`
// pregunta ademas «¿respalda a este principal un hecho de epoca de directorio, sellado y con
// ventana?» (evidence.go, principalAuthorizationEvidence). Un principal sin ese hecho no da
// CheckBroken sino CheckUnknown, y un desconocido no es una denegacion: es ErrRouteUndecided,
// o sea 503 — el API entero caido, no un permiso de menos.
//
// ⇒ LA LECCION, que es lo unico que impide repetirlo: un comentario que afirma equivalencia
// nombra el eje en el que la comprobo. Este nombraba la metadata, seguia siendo cierto sobre
// la metadata, y el sistema habia divergido por otro lado. Una afirmacion de equivalencia
// envejece cuando cambia CUALQUIERA de sus dos lados, no solo el que cita.
//
// ⛔ AND MinimumAAL IS CHECKED HERE, BEFORE THE DECISION, NOT INSIDE THE AUTHORIZER. A
// step-up is a precondition of authentication, not a term of the algebra: folding it in
// would make "prove who you are again" indistinguishable from "you may not do this", two
// answers with different remedies. Checking it BEFORE also means a principal who must step
// up learns nothing about what waited behind it.
func (s *Server) authzTenantResourcePolicy(
	w http.ResponseWriter,
	r *http.Request,
	perm auth.Permission,
	res auth.ResourceAttrs,
	meta auth.RouteMetadata,
	denial error,
	governed routeGovernance,
) (auth.Principal, model.TenantID, auth.RouteAuthorizationWitness, bool) {
	var none auth.RouteAuthorizationWitness
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return auth.Principal{}, "", none, false
	}
	tenant, err := s.resolveTenant(r, p)
	if err != nil {
		s.writeError(w, r, err)
		return auth.Principal{}, "", none, false
	}
	if meta.RequiresStepUp(p.AAL) {
		s.writeError(w, r, auth.ErrStepUpRequired)
		return auth.Principal{}, "", none, false
	}

	// ⛔ EL CAMINO LO ELIGE LA PUERTA, Y NO LA METADATA. Una ruta NO gobernada no declaro
	// politica alguna, asi que exigirle la evidencia sellada del camino de testigo le impone un
	// requisito que nadie escribio para ella — y su fallo no es «un permiso menos», es 503.
	//
	// ⛔ Y NO SE RAMIFICA SOBRE meta.IsZero(), aunque hoy distinguiria los mismos casos. Seria
	// la familia «un valor a cero apaga la comprobacion»: una ruta GOBERNADA cuya metadata
	// quedara vacia —por un refactor, por un literal a medio rellenar— se degradaria al camino
	// booleano en silencio y sin que ningun test lo notara. La gobernanza es una propiedad de
	// la GRAMATICA DE REGISTRO (invariante IV), que es donde no puede derivar a cero sola, y por
	// eso viaja como parametro con tipo propio en vez de deducirse de un valor.
	if !governed {
		if dec := s.authz.Authorize(r.Context(), auth.Request{
			Principal: p, Permission: perm, Tenant: tenant, Resource: res, Route: meta,
		}); !dec.Allow {
			s.writeError(w, r, denial)
			return auth.Principal{}, "", none, false
		}
		return p, tenant, none, true
	}

	// ⛔ AuthorizeRoute Y NO Authorize: ESTA RUTA TIENE QUE PRODUCIR UN TESTIGO. Con el booleano,
	// nada de lo que el testigo liga sobrevivia a la decision, asi que ninguna invariante suya
	// alcanzaba al efecto HTTP (invariante V). Ahora el testigo viaja hasta el handler en el
	// ModuleContext y `CheckRowSet` puede exigir que responda a la pregunta de ESTA peticion.
	// Bound this evidence decision without imposing a lifetime on the handler
	// (which may stream). An existing earlier request deadline remains binding.
	decisionCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	witness, aerr := s.authz.AuthorizeRoute(decisionCtx, auth.Request{
		Principal: p, Permission: perm, Tenant: tenant, Resource: res, Route: meta,
	})
	switch {
	case aerr == nil:
		return p, tenant, witness, true

	// ⛔ EL step-up SIGUE SIENDO SUYO: "vuelve a demostrar quien eres" no es "no puedes", y las
	// dos respuestas tienen remedios distintos. Ya se servia asi antes de este cambio.
	case errors.Is(aerr, auth.ErrStepUpRequired):
		s.writeError(w, r, auth.ErrStepUpRequired)

	// ⛔ EL TERCER ESTADO, que en este camino NO EXISTIA: hasta ahora una decision que no pudo
	// establecerse se servia como denegacion. Va con su propia forma (503) y ANTES de cualquier
	// lectura de fila, para que no pueda correlacionar con la existencia; y la ruta con conceal y
	// la ruta sin conceal responden IGUAL, porque un undecided que distinguiera seria justo el
	// oraculo que el conceal existe para cerrar.
	case errors.Is(aerr, auth.ErrRouteUndecided):
		w.Header().Set("Retry-After", "5")
		s.writeError(w, r, aerr)

	// ⛔ Y LA DENEGACION DE POLITICA SIGUE ESCRIBIENDO EL `denial` QUE LA RUTA PASO. Cambiarlo por
	// el error tipado convertiria en 403 lo que hoy es 404 en toda ruta con
	// ConcealDeniedAsNotFound — y eso no es presentacion: CONFIRMA QUE LA FILA EXISTE. Seria
	// curar el testigo y abrir un oraculo de existencia en el mismo commit.
	default:
		s.writeError(w, r, denial)
	}
	return auth.Principal{}, "", none, false
}

// requireAAL3 is the assurance gate for privileged CONFIGURE actions: SSO
// config, scoped-admin delegation, custom-role edits, user onboarding, workspace
// create/archive. It is ORTHOGONAL to RBAC — assurance never lives in the
// authorizer (algebra) — so a handler calls it as a SECOND, explicit gate
// AFTER the RBAC check (authn → RBAC → AAL3), mirroring the credential-lifecycle
// pattern in core/auth (webauthn.go). A principal below AAL3 gets 403
// step_up_required and the console routes it to the WebAuthn/PIV step-up. A token
// principal (AAL=0, never elevatable) can never pass it, so an AAL3-gated route is
// human-session-only by construction. The principal's AAL is the EFFECTIVE value
// the authenticate middleware already collapsed through the 15-min TTL, so this
// reads the live assurance with no extra store round-trip. For writes that change
// AUTHORITY (grants/roles) the service method re-checks inside its transaction
// (TOCTOU); this edge gate is sufficient for config CRUD.
func (s *Server) requireAAL3(w http.ResponseWriter, r *http.Request, p auth.Principal) bool {
	if p.AAL < auth.AAL3 {
		s.writeError(w, r, auth.ErrStepUpRequired)
		return false
	}
	return true
}

// authzSystem requires a superadmin (the system/cross-tenant role) for a route.
func (s *Server) authzSystem(w http.ResponseWriter, r *http.Request, perm auth.Permission) (auth.Principal, bool) {
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return auth.Principal{}, false
	}
	if dec := s.authz.Authorize(r.Context(), auth.Request{Principal: p, Permission: perm, Tenant: model.SystemTenantID, Resource: auth.ResourceFor(perm)}); !dec.Allow {
		s.writeError(w, r, errForbidden)
		return auth.Principal{}, false
	}
	return p, true
}

// resolveTenant determines the single canonical tenant for an HTTP request from
// the X-Olivares-Tenant header (delegating to the shared resolver).
func (s *Server) resolveTenant(r *http.Request, p auth.Principal) (model.TenantID, error) {
	return s.resolveTenantValue(p, strings.TrimSpace(r.Header.Get("X-Olivares-Tenant")))
}

// resolveTenantValue is the single tenant-resolution rule shared by REST (header)
// and gRPC (request field). It rejects any disagreement between a bound token and
// the supplied tenant, and never resolves the reserved system tenant.
func (s *Server) resolveTenantValue(p auth.Principal, raw string) (model.TenantID, error) {
	var hdr model.TenantID
	if raw != "" {
		t, err := model.ParseTenantID(raw)
		if err != nil || t.IsSystem() {
			return "", errBadRequest
		}
		hdr = t
	}
	// A bound (non-superadmin) token's tenant is authoritative; a header must match.
	if p.Kind == auth.KindToken && !p.Superadmin {
		if ts := p.Tenants(); len(ts) == 1 {
			if hdr != "" && hdr != ts[0] {
				return "", errForbidden
			}
			return ts[0], nil
		}
	}
	if hdr != "" {
		return hdr, nil
	}
	// No tenant given: a single membership defaults; otherwise it must be named.
	if !p.Superadmin {
		if ts := p.Tenants(); len(ts) == 1 {
			return ts[0], nil
		}
	}
	return "", errTenantRequired
}

// authzScopedCollectionPolicy is the ordinary collection flow with ONE step inserted
// where the ratified contract puts it: between the authentication/step-up preconditions
// and the authorization decision, the DECLARED workspace selector is corroborated
// against the stored row, and the corroborated workspace becomes the resource the
// decision is made against.
//
// ⛔ THE ORDER IS THE CONTRACT, NOT A CONVENIENCE. Form first (decided from the request
// alone, no row read), then 401, then step-up, then the lookup, then the decision. A
// caller who must re-authenticate learns nothing about what waited behind it, and an
// infrastructure UNKNOWN cannot appear only after a hidden row has been probed.
//
// ⛔ AND EVERY NON-ADMISSIBLE SCOPE SHARES THE COLLECTION'S OWN GENERIC REFUSAL. Before
// this seam the handler answered 503 for an absent workspace and 403 for an inactive
// one, both AFTER the PEP; moving the resolver in front of the decision would have let
// an authenticated caller with no permission on this collection tell those two apart
// from each other and from a plain denial — by status — before ever being authorized.
// So a known absence, a known non-active status, a crossed confinement and a known outer
// denial are written as the SAME generic 403, with no field that distinguishes them.
//
// ⛔ WHAT DOES NOT COLLAPSE INTO THAT 403: a real store failure and an id/tenant
// integrity mismatch stay 503 UNKNOWN. "I could not look" is not "you may not", they
// have different remedies, and recoding one as the other would fabricate a denial the
// engine never established — the exact substitution the whole projection refuses.
func (s *Server) authzScopedCollectionPolicy(
	w http.ResponseWriter,
	r *http.Request,
	perm auth.Permission,
	meta auth.RouteMetadata,
	scope CollectionScopeRef,
	governed routeGovernance,
) (auth.Principal, model.TenantID, auth.ResourceAttrs, auth.RouteAuthorizationWitness, bool) {
	var (
		none     auth.RouteAuthorizationWitness
		resource = auth.ResourceFor(perm)
	)
	raw, wellFormed := collectionScopeSelector(r, scope)
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return auth.Principal{}, "", resource, none, false
	}
	tenant, err := s.resolveTenant(r, p)
	if err != nil {
		s.writeError(w, r, err)
		return auth.Principal{}, "", resource, none, false
	}
	if meta.RequiresStepUp(p.AAL) {
		s.writeError(w, r, auth.ErrStepUpRequired)
		return auth.Principal{}, "", resource, none, false
	}
	if !wellFormed {
		s.writeError(w, r, errBadRequest)
		return auth.Principal{}, "", resource, none, false
	}
	workspace, admission := s.corroborateActiveWorkspace(r.Context(), p, tenant, raw)
	switch admission {
	case workspaceAdmissionOK:
	case workspaceAdmissionInvalid:
		s.writeError(w, r, errBadRequest)
		return auth.Principal{}, "", resource, none, false
	case workspaceAdmissionNotAdmissible:
		s.writeError(w, r, errForbidden)
		return auth.Principal{}, "", resource, none, false
	default:
		s.writeError(w, r, errEntityAuthorizationUnavailable)
		return auth.Principal{}, "", resource, none, false
	}
	// ⛔ NEVER A FABRICATED OR ZERO WORKSPACE. If corroboration did not succeed the
	// request does not continue with a blank scope "for uniformity": a blank scope is a
	// DIFFERENT authorization question, and answering it would be authorizing to make
	// two refusals look alike.
	resource.WorkspaceID = workspace
	p, tenant, witness, decided := s.authzTenantResourcePolicy(
		w, r, perm, resource, meta, errForbidden, governed,
	)
	return p, tenant, resource, witness, decided
}

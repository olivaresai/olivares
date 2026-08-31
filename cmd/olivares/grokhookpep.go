// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/grok/session"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// grokhookpep.go es el decisor gobernado detrás del PEP de hook de Grok Build. Comparte con el de
// Codex el NÚCLEO de evidencia (`hookpepevidence.go`) y no su código: los dominios de hash, la
// raíz de acción y el proveedor van en el perfil, uno por motor, porque compartirlos haría
// indistinguibles en el registro las decisiones de dos motores.

// grokProfile es el perfil del motor Grok Build.
var grokProfile = hookGovernanceProfile{
	Provider:        sdkmodel.EngineGrok,
	Surface:         grokHookPEPSurface,
	Capability:      grokHookCapability,
	ActionRoot:      "grok.hook",
	OperationDomain: grokOperationDomain,
	EffectDomain:    grokHookEffectDomain,
	DecisionDomain:  grokHookDecisionDomain,
}

// grokHookCapability es la acción de gobierno que abre una llamada gateada, y la raíz del verbo
// del PDP. Deliberadamente NO es la de Codex ni la de Claude.
const grokHookCapability = "grok.tool.use"

// Dominios de hash. Cada uno con su literal: una entrada SIN valor en un bloque `const` repite en
// silencio la expresión anterior, y aquí eso daría a dos dominios el mismo valor y destruiría
// justo la separación que este bloque existe para dar.
const (
	grokHookDecisionDomain = "olivares.grok.hook.decision.v1"
	grokOperationDomain    = "olivares.grok.hook.operation.v1"
	grokHookEffectDomain   = "olivares.grok.hook.effect.v1"
	grokHookPEPSurface     = "grok-hook-pep"
)

// Roles del registro. Un rol dice lo que una entrada ES —la decisión de esta llamada, o el
// registro compensatorio de una que hubo que degradar— y nunca lo que el veredicto FUE.
const (
	grokRoleDecision     = "decision"
	grokRoleCompensation = "compensating-downgrade"
)

const grokReanchorTimeout = 5 * time.Second

type grokSessionResolver interface {
	ResolveSession(ctx context.Context, tenant model.TenantID, b sessions.SessionBinding) (string, error)
}

// grokHookDecider decide una llamada de hook de Grok.
type grokHookDecider struct {
	// tenant es el ÚNICO tenant que gobierna este punto. La pista de cabecera se COMPRUEBA
	// contra él, nunca se usa para elegirlo: dejar que una cabecera entrante escogiera el
	// tenant permitiría a un hook archivar sus decisiones donde quisiera.
	tenant   model.TenantID
	authr    principalAuthenticator
	eval     auth.PolicyEvaluator // nil ⇒ sin superposición externa
	sessions grokSessionResolver
	store    store.Store
	clock    func() time.Time
	log      *slog.Logger
}

var _ session.Decider = (*grokHookDecider)(nil)

// Decide gobierna una llamada de hook de Grok.
//
// El orden importa y es deliberado: la identidad se resuelve ANTES del veredicto, para que una
// llamada denegada aterrice igual en la línea de tiempo de su sesión —una sesión a la que se le
// deniega todo es justo la que un operador más necesita ver—, y el anclaje corre EL ÚLTIMO, sobre
// el veredicto terminal, para que el registro recoja lo que de verdad se contestó.
func (d *grokHookDecider) Decide(ctx context.Context, req session.Request, bearer string) (session.Decision, error) {
	now := d.clock()

	principal, actor, tier := d.principalOf(ctx, bearer)

	// Autorización en TODA petición, con pista o sin ella. La forma de saltarse la
	// autorización no puede ser mandar NADA.
	if tier != tierFirm {
		return grokDeny("no authenticated principal on a governed hook call (deny-closed)", ""), nil
	}
	// staticcheck QF1001 propone De Morgan aquí. NO se aplica, y la razón es de seguridad, no de
	// gusto: esta guarda es deny-closed y la forma actual —una disyunción POSITIVA bajo una sola
	// negación— refleja literalmente la política («hay que ser superadmin O miembro»). La forma
	// `!A && !B` es equivalente hoy y pierde esa forma: quien añada una tercera condición mañana
	// tiene que acordarse de negarla también. Cero cambio de comportamiento a cambio de un riesgo
	// real en la comprobación de pertenencia.
	//nolint:staticcheck // QF1001: la disyunción positiva refleja la política; ver arriba
	if !(principal.Superadmin || principal.IsMember(d.tenant)) {
		return grokDeny("principal is not a member of this endpoint's tenant (deny-closed)", ""), nil
	}
	// Una pista sólo puede CONFIRMAR el tenant que este punto gobierna; jamás seleccionarlo.
	hintTID, hintPresent, hintErr := parseBusinessTenant("grok hook request: identity tenant", req.Identity.Tenant)
	if hintErr != nil || (hintPresent && hintTID != d.tenant) {
		return grokDeny("tenant hint does not match this endpoint's tenant", ""), nil
	}

	sid, ierr := d.resolveSID(ctx, req, now)
	if ierr != nil {
		// Un fallo de identidad no es licencia para seguir: sin sid la llamada no se puede
		// atribuir, registrar ni auditar después, y una acción gobernada inatribuible es
		// exactamente lo que este plano existe para impedir.
		d.logw("grok-hook: could not resolve session identity", "err", ierr)
		return grokDeny("session identity could not be resolved (deny-closed)", ""), nil
	}

	dec := session.Decision{
		Verdict:    session.VerdictAllow,
		SessionSID: sid,
		// Enforced dice sólo «esto lo produjo el decisor GOBERNADO», que aquí es cierto por
		// definición. Si además IMPIDE algo es otra pregunta, y la contesta el conector con
		// `CanVeto`: un deny sobre `post_tool_use` es gobernado y llega después de que la
		// herramienta corriera. Fundir las dos aquí haría la distinción irrecuperable.
		Enforced: true,
	}
	if forbidden, reason := d.pdpForbidsGrok(ctx, principal, req); forbidden {
		dec.Verdict = session.VerdictDeny
		dec.Reason = reason
	}

	return d.anchorGrokDecision(ctx, req, dec, actor), nil
}

// resolveSID convierte el id de sesión de Grok en el sid canónico. El proveedor es la constante
// «grok» — NUNCA se toma del payload, porque un proveedor que el llamante elige es un proveedor
// que el llamante puede suplantar, y toda la garantía de no colisión descansa en que el proveedor
// forme parte de la clave del alias.
func (d *grokHookDecider) resolveSID(ctx context.Context, req session.Request, now time.Time) (string, error) {
	if d.sessions == nil {
		return "", errors.New("grok-hook: no session identity plane is wired")
	}
	if strings.TrimSpace(req.ExternalSessionID) == "" {
		return "", sessions.ErrNoExternalID
	}
	return d.sessions.ResolveSession(ctx, d.tenant, sessions.SessionBinding{
		Provider:   grokProfile.Provider,
		ExternalID: req.ExternalSessionID,
		Origin:     sessions.OriginObserved,
		At:         now,
	})
}

func (d *grokHookDecider) principalOf(ctx context.Context, bearer string) (auth.Principal, actorRef, string) {
	if d.authr == nil || strings.TrimSpace(bearer) == "" {
		return auth.Principal{}, actorRef{}, tierUnknown
	}
	p, err := d.authr.Authenticate(ctx, bearer)
	if err != nil || p.IsPurposeRestricted() {
		return auth.Principal{}, actorRef{}, tierUnknown
	}
	actor, kind := codexActorOf(p)
	return p, actorRef{name: actor, kind: kind}, tierFirm
}

// pdpForbidsGrok consulta el MISMO PDP compuesto que usa el resto del plano, bajo la capacidad de
// Grok. El evaluador es sólo-restrictivo: nunca puede ensanchar.
//
// ⛔ EL VERBO NO SE ADIVINA. Codex mapea el modo de acceso a `read`/`write`; Grok **no declara el
//
//	modo**, así que aquí el verbo es siempre `use`. Derivarlo del nombre de la herramienta
//	convertiría una suposición en el sujeto de una decisión de política, que es peor que un
//	verbo amplio: una política escrita contra `grok.tool.use:read` creería estar gateando
//	lecturas y estaría gateando lo que nuestro adivino clasificó como tales.
func (d *grokHookDecider) pdpForbidsGrok(ctx context.Context, p auth.Principal, req session.Request) (bool, string) {
	if d.eval == nil {
		return false, ""
	}
	r := auth.Request{
		Principal:  p,
		Permission: auth.Permission(grokProfile.Capability + ":use"),
		Tenant:     d.tenant,
		Resource: auth.ResourceAttrs{
			Kind: "grok.tool",
			ID:   req.ResourceRef,
			Extra: map[string]string{
				"tool":            req.Tool,
				"engine":          grokProfile.Provider,
				"event":           req.Event,
				"permission_mode": req.PermissionMode,
			},
		},
	}
	dec, err := d.eval.Evaluate(ctx, r)
	if err != nil {
		return true, "policy evaluation error (deny-closed)"
	}
	if !dec.Allow {
		reason := dec.Reason
		if reason == "" {
			reason = "denied by policy"
		}
		return true, reason
	}
	return false, ""
}

// anchorGrokDecision registra el veredicto terminal y aplica la regla que el precedente estableció
// y ésta conserva: un ALLOW que no se pudo anclar se DEGRADA A DENY, mientras que un DENY que no
// se pudo anclar SE MANTIENE y grita.
func (d *grokHookDecider) anchorGrokDecision(ctx context.Context, req session.Request, dec session.Decision, actor actorRef) session.Decision {
	receipt := d.anchorOnce(ctx, req, dec, actor, grokRoleDecision)
	binding := d.bindingFor(req, dec, actor, grokRoleDecision)
	if !receipt.MustRefuse(binding) {
		return dec
	}
	if dec.Verdict != session.VerdictAllow {
		// Un deny sin recibo sigue siendo un deny. Perder la evidencia es grave y se registra
		// como hueco, pero debilitar el veredicto porque el registro flaqueó sería justo al revés.
		d.logw("grok-hook: DENY could not be anchored; the verdict stands and the evidence is missing",
			"event", req.Event, "tool", req.Tool, "session", dec.SessionSID)
		return dec
	}
	d.logw("grok-hook: ALLOW could not be anchored; downgrading to deny",
		"event", req.Event, "tool", req.Tool, "session", dec.SessionSID)
	downgraded := session.Decision{
		Verdict:    session.VerdictDeny,
		Reason:     "evidence unavailable (deny-closed)",
		SessionSID: dec.SessionSID,
		Enforced:   dec.Enforced,
	}
	// Re-anclaje compensatorio sobre un contexto que sobrevive a una petición cancelada: la
	// propia negativa es un acto gobernado y tiene que dejar rastro. El veredicto se devuelve
	// aterrice o no.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grokReanchorTimeout)
	defer cancel()
	_ = d.anchorOnce(rctx, req, downgraded, actor, grokRoleCompensation)
	return downgraded
}

// bindingFor traduce la llamada de Grok al hecho neutro del núcleo compartido.
//
// ⛔ `CallID` va VACÍO y no es un olvido: el payload de Grok no trae identificador por llamada
//
//	—lo comprobé en el sobre del fuente—, así que el discriminador es SIEMPRE el digest del
//	payload. Que sea el mismo valor en los dos campos no autoriza a fundirlos: en Codex son
//	distintos, y el núcleo es de los dos.
func (d *grokHookDecider) bindingFor(req session.Request, dec session.Decision, actor actorRef, role string) sdk.EvidenceBinding {
	return engineEvidenceBinding(grokProfile, d.tenant, engineFact{
		Event:             req.Event,
		Tool:              req.Tool,
		Discriminator:     req.PayloadDigest,
		CallID:            "",
		ExternalSessionID: req.ExternalSessionID,
		ResourceKind:      "grok.tool",
		ResourceRef:       req.ResourceRef,
		PermissionMode:    req.PermissionMode,
		PayloadDigest:     req.PayloadDigest,
		Verdict:           grokVerdictName(dec.Verdict),
		Reason:            dec.Reason,
	}, actor, role)
}

// anchorOnce hace la reclamación única y devuelve un recibo que el llamante clasifica.
func (d *grokHookDecider) anchorOnce(ctx context.Context, req session.Request, dec session.Decision, actor actorRef, role string) sdk.EvidenceReceipt {
	binding := d.bindingFor(req, dec, actor, role)
	if d.store == nil {
		return sdk.ClassifyAnchor(binding, "", false, sdk.EvidenceFaultLedgerUnwired)
	}
	if actor.name == "" {
		// Un acto gobernado sin atribuir sigue teniendo entrada: la superficie misma es el
		// actor de registro, que es más honesto que una columna vacía.
		actor = actorRef{name: "grok-hook", kind: "system"}
	}
	var (
		evidenceRef string
		dropped     bool
	)
	txErr := d.store.Mutate(ctx, d.tenant, func(sc store.Scope) error {
		res, err := sc.EvidenceOperations().Claim(ctx, store.EvidenceClaim{
			OperationID:  string(binding.OperationID),
			EffectDigest: string(binding.EffectDigest),
			Surface:      grokProfile.Surface,
			Action:       engineLedgerAction(grokProfile, grokVerdictName(dec.Verdict), "deny"),
			Actor:        actor.name,
			ActorKind:    actor.kind,
		})
		if err != nil {
			return err
		}
		if res.Dropped {
			// La contabilidad de la pérdida se queda preparada y nada más. Se devuelve nil
			// para que COMMITEE, y se rechaza después: devolver un centinela desde dentro de
			// la transacción es el fallo histórico que esta regla abolió.
			dropped = true
			return nil
		}
		evidenceRef = res.Op.ClaimEvidenceRef
		return nil
	})
	return sdk.ClassifyAnchor(binding, evidenceRef, dropped, engineClassifyStoreFault(txErr))
}

// grokVerdictName traduce el veredicto de Grok —que es un entero, no una cadena como el de
// Codex— al nombre que viaja al registro. Un valor que no reconozca sale «deny»: no saber lo que
// se decidió no puede escribirse como un permiso.
func grokVerdictName(v session.Verdict) string {
	if v == session.VerdictAllow {
		return "allow"
	}
	return "deny"
}

func grokDeny(reason, sid string) session.Decision {
	return session.Decision{Verdict: session.VerdictDeny, Reason: reason, SessionSID: sid}
}

func (d *grokHookDecider) logw(msg string, args ...any) {
	if d.log != nil {
		d.log.Warn(msg, args...)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/grok/session"
	"github.com/olivaresai/olivares/core/envconfig"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// grokhookpepserver.go monta el PEP de hook de Grok en su PROPIO socket de loopback, con la misma
// forma que el de Codex: una configuración JSON de operador seleccionada por una variable, NADA
// montado cuando no está, y un arranque que falla cerrado cuando está y no sirve.
//
// ⛔ Y EL TRANSPORTE SIN TLS ES DELIBERADO, escrito aquí porque hoy costó un hallazgo falso en el
// hermano: el 2026-08-19 se afirmó dos veces (`7660e0fec`, `209d75963`) que el PEP gobernado de
// Claude Code «no puede funcionar contra un plano con CA privada», y se RETIRÓ — el hook no habla
// con un plano remoto. Postea a un endpoint de decisión EN LOOPBACK que corre la decisión del lado
// servidor, igual que el receptor HITL y la pasarela de agentes. En ese diseño `--pin-sha256` y
// `--ca-cert` no significan nada y su ausencia es correcta, no un hueco. Vale idéntico para éste,
// que es el mismo patrón en su propio puerto: el agente ve un endpoint de decisión en localhost y
// nada del interior del motor.
//
// Un nodo sin cablear no sirve, por tanto, ninguna superficie de imposición de Grok — que es el
// valor por defecto seguro: un agente en ese nodo corre sin gobernar Y sin observar, exactamente
// como antes de que esto existiera, en vez de medio gobernado de una forma que nadie puede
// caracterizar.

// defaultGrokHookPEPListen mantiene la superficie de Grok en su propio puerto. NO se comparte con
// la de Codex ni con la de Claude: los tres dialectos de respuesta son distintos, y un socket que
// contestara a varios significaría que una configuración equivocada recibe una respuesta en una
// forma que el agente ignora en silencio — el fallo exacto que este conector existe para impedir.
const defaultGrokHookPEPListen = "127.0.0.1:8449"

// grokSignalSource nombra a este productor en el sobre del bus.
const grokSignalSource = "grok-hook-pep"

// grokPublishTimeout acota la emisión para que un consumidor lento del bus no pueda parar a un
// agente.
const grokPublishTimeout = 2 * time.Second

// grokHookPEPConfig supplies operator provisioning without declaring its own policy:
// the verdict comes from the composed PDP under Grok's capability, keeping policy in
// one place rather than two that could disagree.
//
// It has no `require_firm` switch. On 2026-08-19, Codex's then-existing switch was
// measured to be unused: it was stored, wired and logged, but `Decide` denied every
// non-firm identity without reading it. Claude's `RequireFirm` does govern behavior
// in `modules/sessions/hookpep/claudehookpep.go`. Copying the unused Codex switch here
// would falsely imply that setting it to false disables the requirement. This PEP
// always requires a firm identity and offers no inert switch.
type grokHookPEPConfig struct {
	Listen string `json:"listen"`
	// Tenant es el ÚNICO tenant que gobierna este punto. Un socket, un tenant: así la pista
	// entrante sólo puede CONFIRMARLO, nunca seleccionarlo.
	Tenant string `json:"tenant"`
}

// loadGrokHookPEPConfig lee el JSON opcional de OLIVARES_GROK_HOOK_PEP_CONFIG. Una ruta ausente da
// una configuración vacía (no se monta nada); una ruta dada tiene que ser legible y válida o el
// arranque falla cerrado.
func loadGrokHookPEPConfig() (grokHookPEPConfig, error) {
	path := osGetenv("OLIVARES_GROK_HOOK_PEP_CONFIG")
	if path == "" {
		return grokHookPEPConfig{}, nil
	}
	var cfg grokHookPEPConfig
	if err := loadOperatorJSONConfig("OLIVARES_GROK_HOOK_PEP_CONFIG", path, &cfg); err != nil {
		return grokHookPEPConfig{}, err
	}
	return cfg, nil
}

// buildGrokHookPEPServer construye el servidor, o nil cuando no está configurado. Se construye
// DESPUÉS del arranque para que el autenticador, el PDP y el plano de identidad estén vivos.
func buildGrokHookPEPServer(eng *engine, sess *sessions.Module, log *slog.Logger) (*http.Server, error) {
	cfg, err := loadGrokHookPEPConfig()
	if err != nil {
		return nil, fmt.Errorf("load grok hook PEP operator config: %w", err)
	}
	// Falla cerrado antes que montar una superficie cuyas decisiones se archivarían en ningún
	// sitio sensato: un punto gobernado con el tenant roto es peor que no tener punto. Un tenant
	// AUSENTE es el caso sin aprovisionar y no monta nada, sin error.
	tid, present, err := parseBusinessTenant("grok hook PEP config: tenant", cfg.Tenant)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	if sess == nil {
		return nil, fmt.Errorf("grok hook PEP: the session identity plane is not wired; refusing to mount an endpoint that cannot attribute its decisions")
	}
	dec := &grokHookDecider{
		tenant:   tid,
		authr:    eng.authr,
		eval:     eng.policyEval,
		sessions: sess,
		store:    eng.store,
		clock:    time.Now,
		log:      log,
	}
	// envconfig.Get supplies the event when a truncated body cannot be parsed. Without
	// that fallback, an unknown event would receive the strictest denial response.
	pep := session.NewPEP(dec, grokObserver(eng, tid, log), time.Now, envconfig.Get)
	pep.OnEmitPanic(func(hookEvent string, cause any) {
		log.Error("grok-hook: the observation for a governed decision was LOST to a panic; the verdict stood but the evidence has a gap",
			"event", hookEvent, "cause", fmt.Sprint(cause))
	})
	listen := strings.TrimSpace(cfg.Listen)
	if listen == "" {
		listen = defaultGrokHookPEPListen
	}
	mux := http.NewServeMux()
	mux.Handle("/", pep)
	log.Info("grok hook PEP mounted", "listen", listen, "tenant", tid.String())
	return &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}, nil
}

// grokObserver sube cada hecho gobernado al bus del motor, donde `modules/sessions` lo pliega.
func grokObserver(eng *engine, tenant model.TenantID, log *slog.Logger) session.Observer {
	return func(req session.Request, dec session.Decision) {
		emit := func(o sdkmodel.Observation) {
			if eng == nil || eng.bus == nil {
				return
			}
			// Mejor esfuerzo, y a propósito: a estas alturas el veredicto ya está decidido y
			// anclado. Un bus que no acepte la observación nos cuesta una fila en la vista viva
			// —un hueco visible— pero no puede cambiar retroactivamente lo que se le dijo al
			// agente.
			//
			// ACOTADO. El bus aplica contrapresión por diseño y esta llamada está en el camino
			// crítico del hook: publicar sin límite dejaría que un consumidor lento parase la
			// llamada de herramienta del agente todo lo que quisiera. El intercambio correcto es
			// perder la observación RUIDOSAMENTE antes que retener al agente.
			pctx, cancel := context.WithTimeout(context.Background(), grokPublishTimeout)
			defer cancel()
			if err := eng.bus.Publish(pctx, event.FromObservation(tenant.String(), grokSignalSource, o)); err != nil && log != nil {
				log.Warn("grok-hook: could not publish the session observation; the live view will be missing this fact",
					"event", req.Event, "err", err)
			}
		}
		if edge, ok := session.EdgeFor(req, dec); ok {
			emit(edge)
		}
		if f, ok := session.LifecycleFinding(req, dec); ok {
			emit(f)
		}
		if f, ok := session.DenyFinding(req, dec); ok {
			emit(f)
		}
	}
}

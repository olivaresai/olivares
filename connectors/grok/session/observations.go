// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/olivaresai/olivares/sdk/model"
)

// observations.go convierte una llamada de hook gobernada en los hechos que la plataforma ya
// sabe plegar. Es la dimensión que hace que una sesión de Grok EXISTA para el producto: sin
// esto el PEP decide bien y nadie ve nada, que para un plano de gobierno es casi lo mismo que
// no gobernar.
//
// NO emite ninguna muestra de coste. Una llamada de hook no cuesta nada, y los totales de
// tokens y coste de la vista viva son cifras reales: sintetizar una muestra para que se cree
// una fila sería fabricar datos.
//
// Las dos puertas que `modules/sessions` pliega son el EDGE de origen «session» (avanza la
// herramienta, el recurso y el modo actuales) y el FINDING con `SubjectKind` «session» (el
// ciclo de vida y las negativas). Son las mismas que usa el hermano de Codex, y por eso no se
// inventa una tercera.

// originSession es el valor sobre el que `modules/sessions` clava su pliegue vivo. Cualquier
// otro encamina el edge al inventario y la sesión no aparece nunca.
const originSession = "session"

// signalGrokHook nombra lo que produjo estos hechos. `sdk/model` no tiene constante para un hook
// de agente —la más cercana, `SignalPolicy`, significa una concesión DECLARADA, que esto no es—,
// así que el valor se declara aquí en vez de tomar prestado `SignalOTEL`, que sería mentira en
// un campo que lee un auditor.
const signalGrokHook = model.SignalSource("grok_hook")

// resourceKindTool es la clase que se afirma cuando la llamada trae herramienta. Es deliberadamente
// GENÉRICA: el payload de Grok da el NOMBRE de la herramienta, no su naturaleza, y clasificarla
// en «shell» o «file» a partir del nombre sería adivinar. Una clase inventada es peor que una
// clase amplia, porque la primera se filtra en una consulta y la segunda no engaña a nadie.
const resourceKindTool = "grok.tool"

// PostureForCall dice la postura HONESTA de una llamada concreta.
//
// ⛔ NO es `Posture(event)`, y la diferencia es la que un auditor necesita. `Posture` contesta
//
//	sobre el EVENTO —«¿admite este evento un veto que el agente honre?»— y es lo que la consola
//	usa para prometer cobertura. Esto contesta sobre esta LLAMADA: hace falta además que la
//	decisión fuese GOBERNADA. Un deny-closed emitido porque el decisor no contestó ocurre en un
//	evento con veto y aun así no es una imposición: no hubo política detrás.
//
//	Colapsar las dos preguntas pintaría «enforced» en sesiones donde el PEP no llegó a decidir,
//	que es exactamente el control que no existe.
func PostureForCall(event string, dec Decision) string {
	if dec.Enforced && CanVeto(event) {
		return model.PostureEnforced
	}
	return model.PostureObserved
}

// EdgeFor construye el edge de acceso de una llamada de herramienta. Devuelve false para los
// eventos que NO son un acceso a recurso —`session_start`, `stop`, `notification`…—: inventarles
// un edge inflaría el recuento de llamadas de herramienta de la sesión con cosas que no lo son.
//
// Una llamada DENEGADA produce edge igual. Registrar el intento es el punto: un operador
// necesita ver lo que una sesión INTENTÓ hacer, no sólo lo que consiguió.
func EdgeFor(req Request, dec Decision) (model.EdgeObservation, bool) {
	if dec.SessionSID == "" || req.Tool == "" {
		return model.EdgeObservation{}, false
	}
	return model.EdgeObservation{
		OriginKind:   originSession,
		OriginRef:    dec.SessionSID,
		ResourceKind: resourceKindTool,
		ResourceRef:  req.ResourceRef,
		// El modo se declara DESCONOCIDO y no se adivina. Grok no dice si una herramienta lee o
		// escribe, y deducirlo del nombre convertiría una suposición en un dato de auditoría.
		Mode:   model.AccessMode("unknown"),
		Source: signalGrokHook,
		// Atribuido, no aproximado: el identificador de sesión vino del propio payload de Grok
		// para ESTA llamada, no de un cruce heurístico.
		Confidence: model.ConfidenceAttributed,
		ToolRef:    req.Tool,
		ObservedAt: req.At,
		Labels: map[string]string{
			model.LabelEngine:  model.EngineGrok,
			model.LabelPosture: PostureForCall(req.Event, dec),
		},
	}, true
}

// LifecycleFinding registra un momento del ciclo de vida como hallazgo con ámbito de sesión — la
// segunda puerta que pliega el módulo. Es lo que hace visible el ciclo arranque→…→cierre.
func LifecycleFinding(req Request, dec Decision) (model.FindingReport, bool) {
	if dec.SessionSID == "" {
		return model.FindingReport{}, false
	}
	var titulo string
	switch req.Event {
	case EventSessionStart:
		// El modo de permisos viaja en el título del arranque porque es donde un operador lo
		// busca, y porque `bypassPermissions` en el arranque cambia cómo se lee TODO lo que
		// venga después en esa sesión.
		titulo = fmt.Sprintf("grok session started (%s, permission mode: %s)",
			PostureForCall(req.Event, dec), oDesconocido(req.PermissionMode))
	case EventSessionEnd:
		titulo = "grok session ended"
	default:
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		SubjectKind: "session",
		SubjectRef:  dec.SessionSID,
		Kind:        "session.lifecycle",
		Severity:    model.SeverityInfo,
		Title:       titulo,
		DetailHash:  detailHash(req.Event, model.EngineGrok, req.ExternalSessionID, req.PermissionMode),
		OccurredAt:  req.At,
	}, true
}

// DenyFinding registra una negativa gobernada contra la sesión. Una negativa que sólo existe en
// la transcripción del agente no es evidencia; esto es lo que la pone en la línea de tiempo.
func DenyFinding(req Request, dec Decision) (model.FindingReport, bool) {
	if dec.SessionSID == "" || dec.Verdict == VerdictAllow {
		return model.FindingReport{}, false
	}
	postura := PostureForCall(req.Event, dec)
	// Una negativa IMPUESTA impidió algo: la sesión intentó un acto que la política prohíbe y se
	// la paró. Una observada es un hecho más débil y se gradúa como tal — graduar las dos igual
	// haría imposible encontrar las impuestas.
	sev := model.SeverityMedium
	if postura == model.PostureEnforced {
		sev = model.SeverityHigh
	}
	return model.FindingReport{
		SubjectKind: "session",
		SubjectRef:  dec.SessionSID,
		Kind:        "session.policy.deny",
		Severity:    sev,
		Title: fmt.Sprintf("grok %s denied (%s): %s",
			req.Event, postura, oDesconocido(req.Tool)),
		DetailHash: detailHash(req.Event, req.Tool, req.ResourceRef, dec.Reason),
		OccurredAt: req.At,
	}, true
}

// detailHash es el canal de datos mínimos: el contrato del SDK lleva un hash del detalle, nunca
// el detalle. Los campos van con su longitud delante para que ("ab","c") y ("a","bc") no puedan
// colisionar.
func detailHash(partes ...string) string {
	h := sha256.New()
	for _, p := range partes {
		fmt.Fprintf(h, "%d:%s|", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func oDesconocido(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

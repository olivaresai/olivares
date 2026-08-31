// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/json"

	"github.com/olivaresai/olivares/sdk/model"
)

// render.go es la mitad SALIENTE del cable de hooks de Grok Build, y es el contrato de
// seguridad de este conector — no un detalle de formato.
//
// ⛔ LA DIFERENCIA DE MECANISMO CON CODEX, QUE ES LA DECISIÓN DE DISEÑO DE ESTE FICHERO.
//
// En Codex el veredicto viaja en la FORMA del stdout, y una forma equivocada **se ignora en
// silencio**: el agente continúa y no hay aviso. Por eso el paquete hermano lleva una tabla
// cerrada de mecanismo por evento.
//
// En Grok el lever documentado es el **CÓDIGO DE SALIDA**, y está citado por código:
// «Exit code 0: allows the tool call» · «Exit code 2: denies/blocks the tool call» ·
// «Other codes (timeouts, crashes, malformed output): fail-open», con la frase que lo define,
// *«the failure is recorded in the session but the tool call proceeds»*.
//
// ⇒ Un código no se puede malinterpretar en silencio como una forma sí. Pero el precio es
//   peor por otro lado: **cualquier fallo nuestro es un PERMISO**. Un pánico, un OOM, un
//   timeout, un stdout corrupto — todos aterrizan en «other» y el agente sigue. En Codex un
//   PEP que se cae no emite deny y el agente también sigue, pero aquí está ESCRITO como
//   comportamiento del producto, así que no es un accidente que podamos tratar como remoto.
//
// De ahí las dos reglas de este fichero:
//
//  1. **Sólo se emiten 0 y 2.** Nunca otro código, en ninguna rama, ni siquiera para «error
//     interno»: un error interno que salga con 1 es un permiso concedido por accidente.
//  2. **Un deny que no se puede expresar NO se emite: se declara.** `Render` devuelve
//     `expressed=false` y el llamante tiene que asentarlo. Un deny inexpresable emitido como
//     si fuera enforcement es exactamente la promesa que este producto no puede permitirse.

// Verdict es lo que la política decidió.
type Verdict int

const (
	// VerdictAllow — no interferir.
	VerdictAllow Verdict = iota
	// VerdictDeny — impedir el acto.
	VerdictDeny
)

// Códigos de salida documentados. No hay más: ver la regla 1 de arriba.
const (
	ExitAllow = 0
	ExitDeny  = 2
)

// mech es lo que un evento puede realmente hacer en este agente.
type mech int

const (
	// mechToolVeto — el evento puede impedir la llamada a herramienta. La documentación
	// describe los códigos 0/2 en términos de «the tool call», así que el único evento para
	// el que el veto está DOCUMENTADO es el que precede a esa llamada.
	mechToolVeto mech = iota
	// mechNone — el evento no tiene veto documentado. Observación.
	//
	// ⚠ NO es lo mismo que «no hace nada»: es que **no está documentado que haga algo**, y
	//    tratar «no documentado» como «funciona» es cómo se publica un control que no
	//    controla. Si algún día x.ai documenta un veto para más eventos, se cita y se mueve
	//    la entrada; hasta entonces esta tabla dice lo que se sabe.
	mechNone
)

// mechFor mapea evento → mecanismo. La rama por defecto es la que sostiene el fichero: un
// evento que este conector no conoce NO recibe el mecanismo más permisivo.
func mechFor(event string) mech {
	if event == EventPreToolUse {
		return mechToolVeto
	}
	return mechNone
}

// denyBody es la forma citada literalmente de la documentación:
//
//	{ "decision": "deny", "reason": "Unsafe command detected" }
type denyBody struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

// Render traduce un veredicto al cable.
//
// Devuelve el cuerpo para stdout, el código de salida, y `expressed`: si el veredicto que se
// pidió pudo realmente expresarse en este evento. Un `VerdictDeny` sobre un evento sin veto
// documentado devuelve `expressed=false` **y sale 0**, porque emitir un 2 que el agente no
// honra no impide nada y además deja en el registro la ilusión de que sí.
func Render(event string, v Verdict, reason string) (stdout []byte, code int, expressed bool) {
	if v == VerdictAllow {
		// Un allow es NO INTERFERENCIA: cuerpo vacío y 0. No se emite `{"decision":"allow"}`
		// porque la documentación no lo declara y afirmar un permiso que no se ha concedido es
		// la misma clase de mentira que un deny que no deniega.
		return nil, ExitAllow, true
	}
	if mechFor(event) != mechToolVeto {
		return nil, ExitAllow, false
	}
	body, err := json.Marshal(denyBody{Decision: "deny", Reason: reason})
	if err != nil {
		// ⛔ Un fallo de serialización NO puede convertirse en un código raro: eso sería
		//    fail-open. Se deniega igual, con el cuerpo vacío. El código es lo que manda.
		return nil, ExitDeny, true
	}
	return body, ExitDeny, true
}

// CanVeto indica si el evento admite un deny que el agente honre. Existe para que el llamante
// pueda decidir ANTES de evaluar la política, y para que la consola pueda decir de qué
// eventos puede prometer enforcement y de cuáles sólo observación.
func CanVeto(event string) bool { return mechFor(event) == mechToolVeto }

// Posture traduce un evento a la postura que este motor puede honestamente reclamar sobre él,
// en el vocabulario del plano de sesión (`sdk/model`).
//
// ⛔ NO ES UNA ETIQUETA COSMÉTICA. `PostureEnforced` afirma que el PEP «estuvo en posición de
//
//	rechazar esta llamada y su decisión fue vinculante». En Grok eso sólo es cierto de
//	`PreToolUse`: es el único evento para el que la documentación describe el veto, en términos
//	de «the tool call». Para el resto, el hook ve y no puede impedir.
//
//	Y hay un segundo motivo por el que esto no se puede generalizar hacia arriba: en Grok
//	**cualquier código distinto de 0 y 2 es fail-open**, así que un PEP que se cae concede. Eso
//	NO rebaja la postura de `PreToolUse` —cuando el PEP responde, su 2 es vinculante— pero sí
//	explica por qué el resto no puede prometerse: no hay un código que impida nada allí.
//
//	Una sesión que mezcle las dos pliega a `observed`, que es la regla que `sdk/model` ya
//	documenta. Con este mapa, una sesión de Grok con una sola herramienta gobernada y cualquier
//	otro evento sale `observed`, que es la verdad.
func Posture(event string) string {
	if CanVeto(event) {
		return model.PostureEnforced
	}
	return model.PostureObserved
}

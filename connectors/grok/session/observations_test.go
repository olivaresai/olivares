// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func reqDe(event, tool string) Request {
	return Request{
		Event:             event,
		ExternalSessionID: "grok-uuid",
		Tool:              tool,
		ResourceRef:       tool,
		PermissionMode:    "bypassPermissions",
		At:                time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC),
	}
}

// ⛔ LA POSTURA DE UNA LLAMADA NO ES LA DEL EVENTO, y ésta es la celda que lo fija. Un
// deny-closed emitido porque el decisor no contestó ocurre en un evento CON veto y aun así no es
// una imposición: no hubo política detrás. Pintarlo «enforced» sería enseñar un control que no
// existe.
func TestLaPosturaDeUnaLlamadaExigeLasDosCosas(t *testing.T) {
	t.Parallel()

	gobernada := Decision{Verdict: VerdictDeny, Enforced: true, SessionSID: "sid"}
	if got := PostureForCall(EventPreToolUse, gobernada); got != model.PostureEnforced {
		t.Fatalf("evento con veto + decisión gobernada = enforced, salió %q", got)
	}
	// Mismo evento, decisión NO gobernada.
	if got := PostureForCall(EventPreToolUse, Decision{Verdict: VerdictDeny, SessionSID: "sid"}); got != model.PostureObserved {
		t.Fatalf("sin decisión gobernada no hay imposición, salió %q", got)
	}
	// Y la FAMILIA entera por el otro lado: ningún evento sin veto puede salir enforced, ni
	// siquiera con la decisión gobernada. Una celda con un solo evento la satisface un mapa que
	// devuelva enforced para todo menos ése.
	for _, e := range KnownEvents() {
		if e == EventPreToolUse {
			continue
		}
		if got := PostureForCall(e, gobernada); got != model.PostureObserved {
			t.Fatalf("%q no admite veto y salió %q", e, got)
		}
	}
	// Control positivo de la familia: si `KnownEvents` viniera vacía el bucle pasaría solo.
	if len(KnownEvents()) < 2 {
		t.Fatal("el barrido de familia no examinó nada")
	}
}

// Un evento de ciclo de vida NO es un acceso a recurso: inventarle un edge inflaría el recuento
// de llamadas de herramienta de la sesión con cosas que no lo son.
func TestSoloLasLlamadasDeHerramientaProducenEdge(t *testing.T) {
	t.Parallel()

	dec := Decision{Verdict: VerdictAllow, Enforced: true, SessionSID: "sid"}
	if _, ok := EdgeFor(reqDe(EventSessionStart, ""), dec); ok {
		t.Fatal("un session_start no es un acceso a recurso")
	}
	e, ok := EdgeFor(reqDe(EventPreToolUse, "Bash"), dec)
	if !ok {
		t.Fatal("una llamada de herramienta SÍ produce edge")
	}
	if e.OriginKind != originSession || e.OriginRef != "sid" {
		t.Fatalf("el edge tiene que clavar el pliegue vivo: %+v", e)
	}
	if e.Labels[model.LabelEngine] != model.EngineGrok {
		t.Fatalf("el edge tiene que decir de qué motor viene: %+v", e.Labels)
	}
	// ⛔ El modo NO se adivina: Grok no dice si una herramienta lee o escribe, y deducirlo del
	//    nombre convertiría una suposición en un dato de auditoría.
	if e.Mode != model.AccessMode("unknown") {
		t.Fatalf("el modo se declara desconocido, no se adivina: %q", e.Mode)
	}
}

// Una llamada DENEGADA produce edge igual: registrar el intento es el punto. Un operador
// necesita ver lo que una sesión INTENTÓ hacer, no sólo lo que consiguió.
func TestUnaLlamadaDenegadaTambienProduceEdge(t *testing.T) {
	t.Parallel()

	den := Decision{Verdict: VerdictDeny, Enforced: true, SessionSID: "sid"}
	e, ok := EdgeFor(reqDe(EventPreToolUse, "Bash"), den)
	if !ok {
		t.Fatal("un intento denegado tiene que quedar registrado")
	}
	if e.Labels[model.LabelPosture] != model.PostureEnforced {
		t.Fatalf("este deny SÍ impidió algo y debe decirlo: %+v", e.Labels)
	}
}

// Sin sesión canónica no se emite NADA por ninguna de las tres puertas: una fila con referencia
// vacía la descarta la vista viva y parecería un hecho entregado.
func TestSinSesionCanonicaNoSeEmiteNada(t *testing.T) {
	t.Parallel()

	dec := Decision{Verdict: VerdictDeny, Enforced: true}
	if _, ok := EdgeFor(reqDe(EventPreToolUse, "Bash"), dec); ok {
		t.Fatal("edge sin sesión")
	}
	if _, ok := LifecycleFinding(reqDe(EventSessionStart, ""), dec); ok {
		t.Fatal("ciclo de vida sin sesión")
	}
	if _, ok := DenyFinding(reqDe(EventPreToolUse, "Bash"), dec); ok {
		t.Fatal("deny sin sesión")
	}
}

// El arranque lleva el MODO DE PERMISOS en el título, porque es donde un operador lo busca y
// porque `bypassPermissions` cambia cómo se lee todo lo que venga después en esa sesión.
func TestElArranqueLlevaElModoDePermisos(t *testing.T) {
	t.Parallel()

	dec := Decision{Verdict: VerdictAllow, Enforced: true, SessionSID: "sid"}
	f, ok := LifecycleFinding(reqDe(EventSessionStart, ""), dec)
	if !ok {
		t.Fatal("el arranque produce hallazgo de ciclo de vida")
	}
	if !strings.Contains(f.Title, "bypassPermissions") {
		t.Fatalf("el modo de permisos tiene que verse en el título: %q", f.Title)
	}
	if f.SubjectKind != "session" || f.SubjectRef != "sid" {
		t.Fatalf("el hallazgo tiene que colgar de la sesión: %+v", f)
	}
	// Un evento que no es de ciclo de vida no inventa uno.
	if _, ok := LifecycleFinding(reqDe(EventPreToolUse, "Bash"), dec); ok {
		t.Fatal("pre_tool_use no es un momento del ciclo de vida")
	}
}

// ⛔ UNA NEGATIVA IMPUESTA Y UNA OBSERVADA NO PUEDEN GRADUARSE IGUAL: si lo hicieran, encontrar
// las que de verdad pararon algo sería imposible en una lista de miles.
func TestLaGravedadDistingueImpuestaDeObservada(t *testing.T) {
	t.Parallel()

	impuesta, _ := DenyFinding(reqDe(EventPreToolUse, "Bash"),
		Decision{Verdict: VerdictDeny, Enforced: true, SessionSID: "sid"})
	observada, _ := DenyFinding(reqDe(EventStop, "Bash"),
		Decision{Verdict: VerdictDeny, Enforced: true, SessionSID: "sid"})

	if impuesta.Severity != model.SeverityHigh {
		t.Fatalf("una negativa que impidió algo es alta, salió %q", impuesta.Severity)
	}
	if observada.Severity != model.SeverityMedium {
		t.Fatalf("una negativa que no pudo impedir nada no es alta, salió %q", observada.Severity)
	}
	// Y un allow no es una negativa.
	if _, ok := DenyFinding(reqDe(EventPreToolUse, "Bash"),
		Decision{Verdict: VerdictAllow, Enforced: true, SessionSID: "sid"}); ok {
		t.Fatal("un allow no puede producir un hallazgo de negativa")
	}
}

// ⛔ EL HASH LLEVA LONGITUDES POR ESTO, y sin esta celda nadie sabría por qué. Sin el prefijo,
// ("ab","c") y ("a","bc") concatenan igual y dos hechos DISTINTOS colapsarían en uno — que en un
// canal de datos mínimos significa perder una evidencia sin que nada lo señale.
func TestElHashNoColisionaAlMoverUnLimite(t *testing.T) {
	t.Parallel()

	if detailHash("ab", "c") == detailHash("a", "bc") {
		t.Fatal("dos detalles distintos producen el mismo hash: falta el prefijo de longitud")
	}
	// Control positivo: el mismo detalle SÍ da el mismo hash, o la comprobación de arriba la
	// pasaría cualquier función que devolviera un valor distinto cada vez.
	if detailHash("ab", "c") != detailHash("ab", "c") {
		t.Fatal("el hash no es estable")
	}
	// Y el vacío no colisiona con la ausencia.
	if detailHash("") == detailHash() {
		t.Fatal("una cadena vacía no es la ausencia de campo")
	}
}

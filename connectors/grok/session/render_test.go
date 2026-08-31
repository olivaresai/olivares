// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/json"
	"testing"
)

// ⛔ LA CELDA MÁS IMPORTANTE DEL PAQUETE, y es un barrido y no un caso.
//
// La documentación de Grok dice que **cualquier código que no sea 0 ni 2 es FAIL-OPEN**: la
// llamada procede. Así que un `return 1` en cualquier rama de `Render` —incluido un «error
// interno»— es un PERMISO concedido por accidente, y no se vería en ninguna prueba que sólo
// mire el camino feliz.
//
// Se barre el producto CARTESIANO de todos los eventos conocidos (más los desconocidos) por
// todos los veredictos, y se exige que el código esté en {0,2}. Un caso suelto no puede
// prometer eso; este barrido sí.
func TestNingunaCombinacionEmiteUnCodigoQueAbraLaPuerta(t *testing.T) {
	t.Parallel()

	eventos := append(KnownEvents(), "", "EventoQueNoExiste", "pretooluse", "PreToolUse ")
	razones := []string{"", "porque sí", string(make([]byte, 1024))}
	for _, e := range eventos {
		for _, v := range []Verdict{VerdictAllow, VerdictDeny} {
			for _, r := range razones {
				_, code, _ := Render(e, v, r)
				// ⛔ LITERALES 0 Y 2, NO `ExitAllow`/`ExitDeny`, y esto lo escribió una mutación
				//    que SOBREVIVIÓ. La primera versión comparaba contra las constantes: cambiar
				//    `ExitDeny = 2` por `= 1` —que convierte TODOS los denies en fail-open— dejaba
				//    las ocho celdas en verde, porque el oráculo se movía con el sujeto.
				//
				//    Un oráculo sacado de la misma fuente que el actual no puede fallar por lo que
				//    dice vigilar. Los códigos son del PROVEEDOR, están citados en render.go, y por
				//    eso van aquí como números y no como símbolos de este paquete.
				if code != 0 && code != 2 {
					t.Fatalf("evento %q veredicto %v: código %d — cualquier cosa que no sea 0 o 2 es fail-open", e, v, code)
				}
			}
		}
	}
}

// El deny que SÍ se puede expresar: PreToolUse, con la forma citada y el código 2.
func TestDenyEnPreToolUseSaleConLaFormaDocumentada(t *testing.T) {
	t.Parallel()

	out, code, expressed := Render(EventPreToolUse, VerdictDeny, "Unsafe command detected")
	if !expressed {
		t.Fatal("PreToolUse es el único evento con veto documentado y salió como inexpresable")
	}
	if code != 2 {
		t.Fatalf("un deny en PreToolUse tiene que salir 2 (el código que Grok documenta como deny), salió %d", code)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("el cuerpo del deny no es JSON: %v (%q)", err, out)
	}
	if got["decision"] != "deny" {
		t.Fatalf("la documentación cita decision:\"deny\" y salió %v", got["decision"])
	}
	if got["reason"] != "Unsafe command detected" {
		t.Fatalf("la razón no viajó: %v", got["reason"])
	}
}

// ⛔ LA OTRA DIRECCIÓN, y sin ella la de arriba la satisface un Render que emita 2 SIEMPRE.
//
// Un deny sobre un evento sin veto documentado NO se emite como si lo tuviera: sale
// `expressed=false` y código 0. Emitir un 2 que el agente ignora no impide nada y además deja
// en el registro la ilusión de que sí — que es peor que no haberlo intentado.
func TestDenyInexpresableSeDeclaraEnVezDeFingirse(t *testing.T) {
	t.Parallel()

	for _, e := range KnownEvents() {
		if e == EventPreToolUse {
			continue
		}
		out, code, expressed := Render(e, VerdictDeny, "no importa")
		if expressed {
			t.Fatalf("%s: no tiene veto documentado y se declaró expresable", e)
		}
		if code != 0 {
			t.Fatalf("%s: un deny inexpresable no puede salir con %d — sería enforcement fingido", e, code)
		}
		if len(out) != 0 {
			t.Fatalf("%s: un deny inexpresable no puede escribir cuerpo: %q", e, out)
		}
	}
	if CanVeto("EventoQueNoExiste") {
		t.Fatal("un evento desconocido no puede declararse con veto")
	}
	if !CanVeto(EventPreToolUse) {
		t.Fatal("PreToolUse sí tiene veto documentado")
	}
}

// Un allow es NO INTERFERENCIA: cuerpo vacío y 0. No se afirma un permiso que la
// documentación no declara.
func TestAllowNoAfirmaUnPermisoQueNoSeConcede(t *testing.T) {
	t.Parallel()

	for _, e := range append(KnownEvents(), "EventoQueNoExiste") {
		out, code, expressed := Render(e, VerdictAllow, "irrelevante")
		if !expressed {
			t.Fatalf("%s: un allow siempre se puede expresar (es no interferir)", e)
		}
		if code != 0 {
			t.Fatalf("%s: allow salió con %d", e, code)
		}
		if len(out) != 0 {
			t.Fatalf("%s: un allow no debe escribir nada, escribió %q", e, out)
		}
	}
}

// ⛔ Y LAS CONSTANTES SE FIJAN CONTRA EL LITERAL CITADO, que es la otra mitad del arreglo de
//
//	arriba. Con los literales metidos en las aserciones, mutar `ExitDeny` deja de romperlas —
//	pero seguiría rompiendo el PRODUCTO, porque `Render` sí devuelve la constante. Esta celda
//	es la que ata el símbolo a lo que la documentación de x.ai dice, con una línea que nombra
//	qué se rompió si se pone roja.
func TestLasConstantesSonLosCodigosQueGrokDocumenta(t *testing.T) {
	t.Parallel()

	if ExitAllow != 0 {
		t.Fatalf("ExitAllow vale %d; x.ai documenta «Exit code 0: allows the tool call»", ExitAllow)
	}
	if ExitDeny != 2 {
		t.Fatalf("ExitDeny vale %d; x.ai documenta «Exit code 2: denies/blocks the tool call», y cualquier otro código es FAIL-OPEN", ExitDeny)
	}
}

// ⛔ LA POSTURA ES UNA AFIRMACIÓN, NO UNA ETIQUETA, y esta celda existe porque el valor por
// defecto cómodo (`enforced`) sería una mentira en trece de los catorce eventos.
//
// `PostureEnforced` dice que el PEP estuvo en posición de rechazar y su decisión fue vinculante.
// En Grok eso sólo lo es de `PreToolUse`. Un mapa que devolviera `enforced` de más pintaría en la
// consola sesiones gobernadas que no lo están — y el operador no tendría forma de distinguirlas.
//
// LA MUTACIÓN: devolver `enforced` siempre, o invertir la condición. Las dos direcciones se miden.
func TestLaPosturaSoloEsEnforcedDondeHayVetoDocumentado(t *testing.T) {
	t.Parallel()

	if got := Posture(EventPreToolUse); got != "enforced" {
		t.Fatalf("PreToolUse tiene veto documentado: posture = %q, quiere \"enforced\"", got)
	}
	for _, e := range KnownEvents() {
		if e == EventPreToolUse {
			continue
		}
		if got := Posture(e); got != "observed" {
			t.Fatalf("%s no tiene veto documentado y su posture salió %q — la consola lo pintaría como gobernado", e, got)
		}
	}
	// Un evento desconocido tampoco puede reclamar enforcement.
	if got := Posture("EventoQueNoExiste"); got != "observed" {
		t.Fatalf("un evento desconocido salió %q", got)
	}
	// Y los literales son los del plano de sesión, no cadenas propias: si `sdk/model` los
	// renombra, esto se pone rojo en vez de emitir un vocabulario paralelo en silencio.
	if Posture(EventPreToolUse) == Posture(EventStop) {
		t.Fatal("las dos posturas colapsaron al mismo valor — el mapa no distingue nada")
	}
}

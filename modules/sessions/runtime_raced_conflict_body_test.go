// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestARacedLaunchAnswers409WithTheRowInTheBody cubre la mitad VISIBLE de la decisión 1: lo que
// el cliente recibe de verdad.
//
// El testigo de `racedFinalizeDTO` prueba que el error lleva la fila; éste prueba que **sale por
// el cable** como 409 + la fila. Son dos cosas distintas y la segunda es la que ve el operador:
// un error bien construido que la capa HTTP renderiza como `errorBody` habría tirado la fila sin
// que ningún test del módulo se enterara.
//
// ⛔ ES LA MISMA LECCIÓN QUE ME COSTÓ `#1716`: allí el campo viajaba en el error y `writeWorkError`
// lo tiraba, y mi testigo no lo vio porque ejercitaba el camino que yo tenía en la cabeza. Aquí el
// camino se ejercita entero, hasta el cuerpo.
func TestARacedLaunchAnswers409WithTheRowInTheBody(t *testing.T) {
	t.Parallel()

	fila := runDTO{RunRef: "run_abc123", State: stateStopped, Reason: "exit"}
	rec := httptest.NewRecorder()
	writeRunErr(rec, &racedRunErr{dto: fila})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, quería %d: un 201 por una corrida que no se creó obliga a cada "+
			"cliente a mirar el cuerpo para descubrirlo, y el que no lo mire la da por buena",
			rec.Code, http.StatusConflict)
	}
	var cuerpo map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &cuerpo); err != nil {
		t.Fatalf("el cuerpo del 409 no es JSON legible: %v — %s", err, rec.Body.String())
	}
	if got := cuerpo["run_ref"]; got != "run_abc123" {
		t.Fatalf("el 409 llegó con run_ref=%v: un 409 PELADO tira el dato que el llamante "+
			"necesita y le obliga a un segundo viaje, que es justo lo que la decisión evita", got)
	}
	if got := cuerpo["state"]; got != string(stateStopped) {
		t.Fatalf("el 409 llegó con state=%v, quería %q: el cuerpo tiene que ser el estado REAL "+
			"de la fila, no un mensaje de error", got, stateStopped)
	}
	if _, esError := cuerpo["error"]; esError {
		t.Fatal("el cuerpo salió como `errorBody`: entonces la fila se ha tirado en la capa " +
			"HTTP y el 409 vuelve a obligar a un segundo viaje")
	}
}

// TestAnOrdinaryRunErrorStillRendersAsAnErrorBody — el CONTROL INVERSO.
//
// Sin él, «el 409 lleva la fila» y «TODO error lleva una fila» se ven igual. Un runErr corriente
// —404, 502— debe seguir saliendo como `errorBody`: no hay fila que dar y fabricar una sería peor
// que el mensaje.
func TestAnOrdinaryRunErrorStillRendersAsAnErrorBody(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	writeRunErr(rec, notFoundErr())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, quería 404", rec.Code)
	}
	var cuerpo map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &cuerpo); err != nil {
		t.Fatalf("cuerpo ilegible: %v", err)
	}
	if _, hay := cuerpo["error"]; !hay {
		t.Fatalf("un 404 salió sin `error`: %s — el caso del 409 con fila es la EXCEPCIÓN, "+
			"no la regla nueva", rec.Body.String())
	}
}

// TestTheRacedErrorSurvivesBeingJoined — el caso que el camino de fallo produce de verdad.
//
// Cuando el desmontaje también falla, `racedFinalizeDTO` devuelve `errors.Join(raced, teardownErr)`.
// Si el render mirase el tipo concreto en vez de usar `errors.As`, ese caso perdería la fila
// justo cuando más contexto hace falta.
func TestTheRacedErrorSurvivesBeingJoined(t *testing.T) {
	t.Parallel()
	fila := runDTO{RunRef: "run_join", State: stateStopped}
	err := errors.Join(&racedRunErr{dto: fila}, errors.New("teardown también falló"))
	rec := httptest.NewRecorder()
	writeRunErr(rec, err)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d con el error UNIDO, quería 409: el render tiene que usar "+
			"errors.As y no el tipo concreto", rec.Code)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"sync"
	"testing"
)

// TestTheReservationWindowDefersRowWritesAndFlushesThemInOrder es el testigo que INVIERTE con la
// opción 4 — el que prueba que el cambio HACE algo, frente a los tres de no-regresión que prueban
// que no rompe nada.
//
// La ventana existe porque entre `go m.bridge(lr)` y la transición que reserva la fila
// (`launched`/`resumed`) el bridge ya puede escribir, y su escritura compite con el CAS de esa
// transición. La opción 4 sigue consumiendo igual y difiere **sólo** los efectos que tocan la fila.
//
// ⛔ SE PRUEBA EL MECANISMO, NO LA CARRERA. Un test que reproduzca la carrera real es intermitente
// por construcción, y un rojo aleatorio en un check requerido enseña a la gente a ignorar el job
// entero — la misma razón por la que el testigo de `#1685` fija la PROPIEDAD y no la carrera.
func TestTheReservationWindowDefersRowWritesAndFlushesThemInOrder(t *testing.T) {
	t.Parallel()

	lr := &liveRun{}
	var aplicados []string
	var mu sync.Mutex
	efecto := func(nombre string) func() {
		return func() {
			mu.Lock()
			defer mu.Unlock()
			aplicados = append(aplicados, nombre)
		}
	}
	leer := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), aplicados...)
	}

	// ANTES de abrir: se aplica directo, que es el comportamiento de siempre.
	if lr.difiereSiLaReservaSigueAbierta(efecto("antes")) {
		t.Fatal("difirió con la ventana CERRADA: fuera de la reserva no hay nada que diferir, " +
			"y retrasar escrituras que no compiten con nada sería coste sin motivo")
	}

	lr.abreVentanaDeReserva()
	for _, n := range []string{"uno", "dos", "tres"} {
		if !lr.difiereSiLaReservaSigueAbierta(efecto(n)) {
			t.Fatalf("NO difirió %q con la ventana abierta: ese efecto compite con el CAS de la "+
				"reserva, que es exactamente la carrera que la opción 4 cierra", n)
		}
	}
	if got := leer(); len(got) != 0 {
		t.Fatalf("se aplicaron %v durante la ventana: diferir significa NO tocar la fila hasta "+
			"que la reserva commitee", got)
	}

	lr.cierraVentanaYVuelca()
	got := leer()
	quiero := []string{"uno", "dos", "tres"}
	if len(got) != len(quiero) {
		t.Fatalf("tras cerrar se aplicaron %v, quería %v: un efecto que se difiere y no se vuelca "+
			"NO se ha retrasado, se ha PERDIDO — y `last_activity_at` se quedaría congelado en el "+
			"instante del lanzamiento", got, quiero)
	}
	for i := range quiero {
		if got[i] != quiero[i] {
			t.Fatalf("orden de volcado = %v, quería %v: el orden importa porque el último sello "+
				"gana, y volcarlos al revés dejaría el más viejo", got, quiero)
		}
	}

	// Y tras cerrar, vuelve a aplicarse directo.
	if lr.difiereSiLaReservaSigueAbierta(efecto("despues")) {
		t.Fatal("siguió difiriendo tras cerrar: la ventana no se habría cerrado nunca")
	}
}

// TestTheWindowFlushesEvenWhenTheReservationFails — el caso que un arreglo apresurado se deja.
//
// El volcado va en `defer`, así que ocurre TAMBIÉN cuando la transición de reserva falla. Si sólo
// se volcara en el camino feliz, un lanzamiento que pierde la carrera dejaría los efectos
// encolados para siempre: la fila se quedaría con el sello del lanzamiento y un barrido de
// inactividad podría matar una sesión viva. Diferir sin garantizar el volcado no es retrasar: es
// perder.
func TestTheWindowFlushesEvenWhenTheReservationFails(t *testing.T) {
	t.Parallel()
	lr := &liveRun{}
	volcado := 0
	lr.abreVentanaDeReserva()
	lr.difiereSiLaReservaSigueAbierta(func() { volcado++ })
	// se cierra igual que lo haría el `defer` de un camino que devuelve error
	lr.cierraVentanaYVuelca()
	if volcado != 1 {
		t.Fatalf("volcados=%d, quería 1: el cierre va en `defer` justamente para que un fallo de "+
			"la reserva no deje efectos encolados", volcado)
	}
}

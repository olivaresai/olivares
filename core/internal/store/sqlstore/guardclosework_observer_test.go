// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"testing"
	"time"
)

// THE SEAM MUST NOT BE INSTALLED IN PRODUCTION, and that is checkable rather than promised.
//
// guardCloseWorkSpentObserver is nil by default, so the close never even builds the defer.
// A future edit that wires it at package init — for a metric, for a log — would put a
// callback of unknown cost on every guard close, and this is what would say so.
func TestTheWorkSpentObserverIsNotWiredByDefault(t *testing.T) {
	if guardCloseWorkSpentObserver != nil {
		t.Fatal("guardCloseWorkSpentObserver is set at package level: production closes now " +
			"carry an observer, which is exactly what this seam was allowed in on the promise " +
			"of not doing")
	}
}

// AND OBSERVING MUST NOT CONSUME WHAT IT OBSERVES.
//
// The clock is frozen on purpose: with a real one, `remaining()` shrinks between reads and a
// change that DID consume budget would be indistinguishable from time passing. Frozen, any
// movement at all is the observation's own doing.
func TestObservingTheWorkBudgetDoesNotConsumeIt(t *testing.T) {
	frozen := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	g := &guardCloseBudgets{now: func() time.Time { return frozen }}
	// Por `workBudget()` y no a mano: el estado que se observa tiene que ser el que produce la
	// produccion, no uno que se le parezca.
	g.workBudget()

	before := g.work.remaining()
	first := g.workSpent()
	for i := 0; i < 50; i++ {
		if got := g.workSpent(); got != first {
			t.Fatalf("read %d reported %s where the first read said %s: observing moved the budget",
				i, got, first)
		}
	}
	if after := g.work.remaining(); after != before {
		t.Fatalf("the budget went from %s left to %s left across 51 observations: reading consumed it",
			before, after)
	}
}

// AND EVERY BUDGET THE CLOSE CREATES MUST STAY IN THE ACCOUNTING.
//
// ⛔ ESTE CONTROL EXISTE PORQUE SU PRIMERA VERSION MIRABA HACIA OTRO LADO, y el coste fue el
// detector entero. Aquella contabilidad tenia un campo `workRetired` que se sumaba en un
// `retireWorkBudget()` — y a ese ayudante no lo llamaba NADIE fuera de este fichero. O sea que
// el control verificaba una mutacion que se acordaba de banquear, cuando la mutacion que se
// escribe de verdad es soltar el puntero y ya:
//
//	budgets.work = nil   // presupuesto por intento, en el bucle
//
// Medido contra Postgres el 2026-08-24, con esa version:
//
//	mutante realista   work_spent=715ms   elapsed=2.415s   ->  PASS   (SOBREVIVIA)
//	techo viejo (reloj de pared)          2,415 s > 1,8 s  ->  MUERTO
//
// El arreglo habria apagado el unico control que cazaba el defecto, y un control que deja de
// discriminar no avisa de que dejo de discriminar. Por eso la contabilidad ya no es un contador
// que hay que acordarse de actualizar sino la LISTA de lo que `workBudget()` crea, y por eso
// este control suelta el puntero **exactamente como lo haria la mutacion** en vez de invocar un
// ayudante que la mutacion no tiene por que conocer.
func TestEveryWorkBudgetCreatedStaysInTheAccounting(t *testing.T) {
	frozen := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	clock := frozen
	g := &guardCloseBudgets{now: func() time.Time { return clock }}

	// Primer presupuesto: se gasta la mitad y se SUELTA, que es la mutacion real.
	g.workBudget()
	clock = clock.Add(guardCloseWorkBudget / 2)
	g.work = nil

	// Segundo: `workBudget()` crea otro y se gasta otra mitad.
	g.workBudget()
	clock = clock.Add(guardCloseWorkBudget / 2)

	if g.workCreated != 2 {
		t.Fatalf("la contabilidad cuenta %d presupuesto(s) tras crear dos: el registro no ocurre donde se crea", g.workCreated)
	}
	want := guardCloseWorkBudget
	if got := g.workSpent(); got != want {
		t.Fatalf("dos medios presupuestos a un lado y otro de una sustitucion cuentan %s, se esperaba %s: "+
			"un close que rehace su presupuesto reportaria solo el ultimo y el techo no morderia nunca",
			got, want)
	}
}

// AND A CLOSE THAT NEVER REACHED THE WORK PHASE MUST REPORT ZERO, not a made-up number.
func TestAWorkPhaseNeverReachedSpendsNothing(t *testing.T) {
	g := &guardCloseBudgets{}
	if got := g.workSpent(); got != 0 {
		t.Fatalf("un close que no llego a la fase de trabajo reporta %s de gasto", got)
	}
}

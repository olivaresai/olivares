// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// TestTheFinalizeWaitIsABackstopAndNotABudget separa las DOS explicaciones del «did not finalize»
// que the planner dejó abierto el 2026-08-24 con una sola ocurrencia bajo `-race`, y su encargo
// decía: primero un test que lo FUERCE, porque una ocurrencia no es tasa.
//
// LAS DOS EXPLICACIONES, y por qué una muestra no las separa:
//
//	el run NO finaliza          -> hay un cuelgue, y la atribución es del código
//	el run SÍ finaliza TARDE    -> el plazo es lo que corta, y el defecto es del INSTRUMENTO
//
// Reproducirlo esperando a que vuelva la intermitente no distingue ninguna de las dos: sólo
// dice que a veces pasa. Lo que las separa es hacer la pregunta DOS VECES sobre el MISMO run —
// una con un plazo imposible y otra con uno generoso— y comparar las respuestas.
//
// ⛔ POR QUÉ ENCOGER EL PLAZO Y NO RALENTIZAR EL CÓDIGO. Forzarlo por el otro lado exigiría una
// costura de retardo en la ruta de finalize, que NO existe: habría que añadirla a producción
// para probar un test. Encoger el presupuesto no toca producción, es determinista y no depende
// de la carga de la caja — que es justo la variable que hace irreproducible el original.
//
// ⛔ Y SIN `t.Parallel()`, deliberadamente — PERO NO POR LA RAZÓN QUE ESCRIBÍ AQUÍ ANTES.
//
// Dije que este test ASIGNA la variable de paquete `finalizeWaitBudget` y que un gate del
// repositorio lo exigía. **Las dos mitades eran falsas y las retiro.** Un contraste externo las
// atacó y lo comprobé yo con el fichero delante:
//
//	asignaciones a `finalizeWaitBudget` en este fichero:  0  (sólo se LEE)
//	`scripts/check-test-hook-parallelism.sh` en `main`:   NO EXISTE  (es de otra rama)
//	ficheros que usan `newRuntimeHarness` Y `t.Parallel`: 15  (o sea, es normal aquí)
//
// LA RAZÓN VERDADERA, que además es más fuerte: **este test MIDE UN TRAMO DE TIEMPO** y lo
// compara con un plazo. Bajo `t.Parallel()` ese número deja de medir la ruta de finalize y pasa
// a medir la CAJA — la misma variable que hacía irreproducible el original. Un cronómetro
// compartido con 28 vecinos no cronometra el código. Por eso va serial, y por eso la aserción
// de abajo sobre `elapsed` puede ser una aserción y no un aviso.
func TestTheFinalizeWaitIsABackstopAndNotABudget(t *testing.T) {
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()

	// ⛔ LA DIRECCIÓN 1 CORRE UNA CARRERA Y PUEDE PERDERLA — Y AL PERDERLA, ESTE TEST ACUSABA AL
	// CÓDIGO. Lo señaló un contraste externo y lo confirmé con un mutante.
	//
	// `waitWorkRuntimeFinalized` devuelve `true` ÚNICAMENTE por el caso `<-lr.finalizedCh`, así
	// que `true` significa exactamente una cosa: el canal estaba cerrado, o sea que el run SÍ
	// había finalizado. Nunca significa que el plazo fallara. Por eso el `t.Fatal` que había aquí
	// —«con un plazo de 1 ns el run no puede haber finalizado»— era **falso de raíz**: el run sí
	// puede haber finalizado, si la finalización le gana la carrera al test.
	//
	// Y la lectura no bloqueante de abajo NO cierra ese agujero, sólo lo ESTRECHA — escribí que
	// «lo cierra» y lo retiro. Entre su `default` y el `select` de dentro del ayudante queda una
	// ventana; si el planificador desaloja la gorrutina justo ahí, la finalización termina y los
	// DOS casos quedan listos. Go elige entonces uniformemente (spec del `select`), de modo que
	// el rojo salía la mitad de las veces.
	//
	// MEDIDO, no razonado — 20 corridas por columna, mutante que ensancha la ventana con 50 ms:
	//
	//	test tal cual .................  0 rojos de 20
	//	con la ventana ensanchada ..... 10 rojos de 20   <- el 50 % que predice la moneda
	//
	// EL ARREGLO NO ES ESPERAR MÁS NI AFINAR LA GUARDA: una tirada en la que la finalización gana
	// no mide nada, así que se DESCARTA y se repite con un run nuevo. Si se pierden todas, la
	// respuesta correcta es la TERCERA —«no pude mirar»—, y no un rojo que le cuelga al código una
	// carrera del instrumento.
	const intentos = 8
	var lr *liveRun
	perdidas := 0
	for i := 0; i < intentos; i++ {
		dto, err := m.createRun(ctx, tenant, CreateRunParams{
			Transport: TransportStreamJSON, Isolation: IsolationNative,
			Actor: "user:u1", ActorKind: model.ActorUser,
		})
		if err != nil {
			t.Fatalf("createRun: %v", err)
		}
		cand, live := m.rt.getLive(tenant, dto.RunRef)
		if !live {
			t.Fatalf("run %s has no live handle", dto.RunRef)
		}

		// El proceso muere. A partir de aquí la finalización es trabajo REAL: tres viajes al
		// store (runtime_bridge.go:253, :273, :279) antes de que se cierre `finalizedCh`.
		fr.lastProc().finish(0)

		// Si ya finalizó, no hay finalización EN VUELO que cronometrar: tirada descartada.
		select {
		case <-cand.finalizedCh:
			perdidas++
			continue
		default:
		}

		// DIRECCIÓN 1 · con un plazo imposible, el veredicto es «no finalizó».
		veredicto := waitWorkRuntimeFinalized(cand, time.Nanosecond)

		// ⛔ Y LA VALIDEZ DE LA TIRADA SE COMPRUEBA, NO SE SUPONE. Mi primer arreglo daba la
		// tirada por buena en cuanto el veredicto era `false`, y ESO ERA UN VERDE FALSO —lo
		// cazó el mismo mutante que había cazado el rojo falso, ensanchando la ventana 50 ms:
		//
		//	rojo falso (antes del arreglo) ....... 10 rojos de 20
		//	verde falso (arreglo a medias) ....... 20 PASAN de 20, sin medir nada
		//
		// La razón es la misma moneda por el otro lado: si `finalizedCh` YA está cerrado y el
		// plazo YA venció, los dos casos del `select` están listos y Go elige uniformemente.
		// Cuando elige el temporizador devuelve `false` — y `false` sobre un run que ya había
		// finalizado no dice que el plazo cortara nada: no había nada que cortar.
		//
		// Por eso la condición de validez es una POSCONDICIÓN OBSERVADA, no una suposición
		// sobre tiempos: la tirada vale sólo si el veredicto fue `false` **y** el canal sigue
		// abierto, o sea que quedaba finalización EN VUELO que el plazo pudiera cortar.
		abierto := true
		select {
		case <-cand.finalizedCh:
			abierto = false
		default:
		}
		if veredicto || !abierto {
			perdidas++
			continue
		}
		lr = cand
		break
	}
	if lr == nil {
		t.Skipf("NO PUDE MIRAR: en %d intentos la finalización ganó SIEMPRE la ventana entre la "+
			"guarda y la espera de 1 ns, así que no hubo ninguna finalización en vuelo que "+
			"cronometrar. No es un rojo —no acusa al código— pero tampoco es un verde: si esto "+
			"pasa de forma sostenida, la finalización se ha vuelto tan rápida que este test ya no "+
			"separa las dos explicaciones y hay que rediseñarlo", intentos)
	}
	// DIRECCIÓN 2 · el MISMO run, con un plazo generoso, SÍ finaliza.
	//
	// Aquí está la carga de la prueba: si el veredicto cambia sin que cambie nada del run, lo
	// que decidía era el PLAZO. Si no cambiara, habría un cuelgue de verdad y la atribución
	// sería del código — que es la otra explicación, y ésta es la que la descartaría.
	start := time.Now()
	ok := waitWorkRuntimeFinalized(lr, 30*time.Second)
	elapsed := time.Since(start)

	// ⛔ EL LOG DE TIRADAS DESCARTADAS VA AQUÍ, DESPUÉS DE PARAR EL CRONÓMETRO. Estaba entre la
	// poscondición de validez y `start`, así que su E/S caía DENTRO de la ventana medida y
	// `elapsed` infra-informaba — la dirección insegura, porque `elapsed >= budget` es lo que
	// tiene que poder enrojecer.
	if perdidas > 0 {
		t.Logf("FINALIZE_WAIT|tiradas_descartadas=%d|de=%d", perdidas, intentos)
	}
	if !ok {
		t.Fatal("el run NO finaliza ni con 30 s: entonces no es un plazo apretado, es un " +
			"cuelgue real en la ruta de finalize y la atribución sí es del código")
	}

	// ⛔ Y SE PUBLICA EL TRAMO, que es la cifra que el diagnóstico necesitaba y no tenía.
	// Medir el test entero no sirve: incluye `createRun` y el montaje. Esto es SÓLO la
	// finalización, y es lo único comparable con los 3 s de `finalizeWaitBudget`.
	t.Logf("FINALIZE_WAIT|elapsed=%s|budget=%s|margen=%s", elapsed, finalizeWaitBudget,
		finalizeWaitBudget-elapsed)
	if elapsed >= finalizeWaitBudget {
		// ⛔ ESTO ERA UN `t.Logf` Y POR TANTO NO ERA NADA. Lo cazó un contraste externo y es
		// mi propia trampa fichada: *un número que sólo avisa*. El test podía PASAR en el
		// caso exacto que existe para detectar — el tramo de finalize cruzando el plazo por
		// omisión — porque escribía una línea y seguía. Un control que no puede enrojecer
		// no es un control; es documentación con formato de código.
		//
		// Ahora falla, y puede hacerlo porque el test es SERIAL: el número mide la ruta de
		// finalize, no la carga de la caja. Si esto se pone en paralelo, esta aserción hay
		// que quitarla en el MISMO cambio — medirían cosas distintas.
		t.Fatalf("el tramo de finalize (%s) CRUZA el plazo por omisión (%s): aquí el "+
			"«did not finalize» no es intermitente, es seguro — y el plazo es el sospechoso, "+
			"no el código", elapsed, finalizeWaitBudget)
	}
}

// TestTheFinalizeBudgetIsNotRaisedToSilenceARed fija el valor por omisión.
//
// No vigila un comportamiento: vigila una TENTACIÓN. Cuando este plazo corte de nuevo bajo carga,
// la reacción barata es subirlo — y subirlo convierte un rojo ruidoso en una espera larga que
// acaba en el mismo sitio, sin que nadie vuelva a mirar. Si hay que cambiarlo, que sea con este
// test delante y con la medida que lo justifique, no de pasada.
func TestTheFinalizeBudgetIsNotRaisedToSilenceARed(t *testing.T) {
	if finalizeWaitBudget != 3*time.Second {
		t.Fatalf("finalizeWaitBudget = %s, y estaba en 3s. Si lo has subido para acallar un "+
			"rojo, el rojo sigue ahí y ahora tarda más en aparecer: mide la ruta de finalize "+
			"(tres viajes al store) antes de mover este número", finalizeWaitBudget)
	}
}

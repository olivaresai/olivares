// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ⛔ ESTOS DOS TESTIGOS CUBREN EL RÉGIMEN QUE T3 NO MIDE, y existen porque el integrador leyó la PR
// y encontró que mi frase «el productor nunca se bloquea» era cierta para UN régimen y falsa para
// el otro.
//
//	ventana ABIERTA  -> los efectos se ENCOLAN, no tocan la base. Eso mide T3.
//	VOLCADO          -> `cierraVentanaYVuelca` corre las escrituras CON `aplicaMu` tomado,
//	                    y `onStdout` toma ESE MISMO mutex ⇒ el productor SÍ espera.
//
// Es exactamente «un instrumento correcto para SU pregunta es falso para otra», cometido por mí y
// cazado leyendo el código, no el mensaje.

// TestTheFlushBlocksTheProducerOnlyForItsOwnDuration mide el régimen del volcado: el productor
// espera, y espera ACOTADO por lo que dura el volcado — no indefinidamente.
//
// Se mide, no se afirma: el testigo cronometra la espera y la compara con la duración del volcado.
// Va SIN `t.Parallel()` porque mide un tramo de tiempo, igual que `runtime_finalize_wait_test.go`.
func TestTheFlushBlocksTheProducerOnlyForItsOwnDuration(t *testing.T) {
	lr := &liveRun{}
	const k = 8
	const porEfecto = 20 * time.Millisecond

	lr.abreVentanaDeReserva()
	for i := 0; i < k; i++ {
		lr.difiereSiLaReservaSigueAbierta(func() { time.Sleep(porEfecto) })
	}

	volcadoEmpezo := make(chan struct{})
	var unaVez sync.Once
	go func() {
		unaVez.Do(func() { close(volcadoEmpezo) })
		lr.cierraVentanaYVuelca()
	}()
	<-volcadoEmpezo

	// El productor: toma el MISMO mutex que el volcado, que es lo que hace `onStdout`.
	inicio := time.Now()
	lr.aplicaMu.Lock()
	espera := time.Since(inicio)
	lr.aplicaMu.Unlock()

	techo := k*porEfecto + 5*time.Second
	if espera > techo {
		t.Fatalf("el productor esperó %s por un volcado de %d efectos: la espera tiene que estar "+
			"ACOTADA por la duración del volcado (%s) y no crecer sin techo", espera, k, k*porEfecto)
	}
	t.Logf("FLUSH_REGIME|efectos=%d|espera_del_productor=%s|techo=%s", k, espera, techo)
}

// TestTheFlushCollapsesQueuedActivityIntoOneRowWrite es la respuesta MEDIDA a «¿puede N crecer sin
// techo?» — la pregunta que decide si el volcado debe salir de `aplicaMu`.
//
// N efectos encolados NO son N escrituras de fila: `touchActivity` está estrangulado a
// `activityWriteInterval` (10 s, runtime_bridge.go:247) y durante la ventana no se escribe, así que
// `lastActivityWrite` sigue a cero; al volcar, el PRIMERO escribe y los demás salen por el
// estrangulador SIN tocar la base. `captureSessionID` escribe una sola vez (`sessionIDCaptured`,
// :127).
//
// Se cuenta con la VERSIÓN de la fila, que sube en cada `Update`: el delta ES el número de
// escrituras. No es una inferencia sobre el estrangulador, es la cuenta del store.
func TestTheFlushCollapsesQueuedActivityIntoOneRowWrite(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatalf("run %s has no live handle", dto.RunRef)
	}

	version := func() int64 {
		var v int64
		if err := m.data.View(ctx, lr.tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			rec, err := findRunRec(ctx, repo, lr.runRef)
			if err != nil {
				return err
			}
			v = rec.Int(model.ColVersion)
			return nil
		}); err != nil {
			t.Fatalf("leer version: %v", err)
		}
		return v
	}

	const k = 20
	base := time.Now()
	antes := version()
	lr.abreVentanaDeReserva()
	for i := 0; i < k; i++ {
		at := base.Add(time.Duration(i) * time.Millisecond) // TODOS dentro del estrangulador
		lr.difiereSiLaReservaSigueAbierta(func() { m.touchActivity(ctx, lr, at) })
	}
	lr.cierraVentanaYVuelca()
	escrituras := version() - antes

	if escrituras > 2 {
		t.Fatalf("volcar %d efectos costó %d escrituras de fila: si N escrituras crecen con N "+
			"efectos, el volcado bloquea al productor un tiempo proporcional a N y hay que "+
			"sacarlo de `aplicaMu`", k, escrituras)
	}
	t.Logf("FLUSH_WRITES|efectos_encolados=%d|escrituras_de_fila=%d|estrangulador=%s",
		k, escrituras, activityWriteInterval)
}

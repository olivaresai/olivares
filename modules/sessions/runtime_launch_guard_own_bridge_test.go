// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// TestTheLaunchGuardDoesNotFenceARunFromItsOwnBridge fija el ESLABÓN que hace posible que el
// ganador de una reserva pierda su propio CAS, y que hasta el 2026-08-25 nadie había comprobado.
//
// ⛔ QUÉ SE AFIRMA, Y QUÉ NO. Esto NO reproduce la carrera —un test que reproduce una carrera real
// es intermitente por construcción, y un rojo aleatorio en un check requerido enseña a la gente a
// ignorarlo—. Fija la PROPIEDAD que la hace posible, y es determinista:
//
//	`guardRuntimeLaunch` NO protege la fila del bridge DEL PROPIO RUN.
//
// El bridge lleva el MISMO `launch_id` que la fila, así que la guarda **pasa** y su escritura
// **sube la versión**. Si el ganador está a la vez dentro de su `transition`, que releyó antes del
// bump, su CAS muere con el centinela PELADO del store (`version conflict`) — no con
// `conflictErr("runtime launch reservation changed")`, que es lo que daría un fallo de la guarda.
// **Esa distinción de firma es la que cerró el espacio de búsqueda**: sólo un escritor con el
// mismo `launch_id` la produce, y el único es el propio ganador.
//
// Cadena verificada el 2026-08-25 por dos carriles en cruzada (la atribución va en el trailer
// del commit, no aquí: nombrar un carril en el FUENTE sube el trinquete de vocabulario del
// export, y esta línea lo subió de 23 a 25 y tumbó su propio push):
//
//	runtime.go:953           go m.bridge(lr)      arranca ANTES del CAS de :957
//	runtime_test.go:82-85    el frame `init` se ENCOLA en Launch, buffer 16 — no hace falta Wait()
//	runtime_bridge.go        onStdout -> captureSessionID -> mutateRunBest + bindProviderSession
//	                         + renewLaunchClaim  = TRES escrituras por frame
//	runtime.go:1858-1860     transition reintenta UNA vez ⇒ hacen falta DOS colisiones seguidas
//
// ⇒ si algún día se decide que el bridge no escriba durante la reserva, **este test se pone rojo
// y hay que venir aquí a decir por qué** — que es exactamente lo que se quiere de un testigo.
func TestTheLaunchGuardDoesNotFenceARunFromItsOwnBridge(t *testing.T) {
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
		t.Helper() // aquí SÍ: es un ayudante de verdad, con varios llamantes
		rec, err := m.loadRun(ctx, tenant, dto.RunRef)
		if err != nil {
			t.Fatalf("loadRun: %v", err)
		}
		return rec.Int(model.ColVersion)
	}

	before := version()

	// DIRECCIÓN 1 · el bridge del PROPIO run escribe, y la guarda no lo para.
	m.mutateRunBest(ctx, lr, func(rec model.Record) {
		rec[colLastActivityAt] = model.NewTimestamp(m.now()).String()
	})
	afterOwn := version()
	if afterOwn <= before {
		t.Fatalf("el bridge del propio run NO subió la versión (%d -> %d). Si esto ha cambiado, "+
			"la carrera que este testigo documenta puede estar cerrada — y entonces hay que "+
			"actualizar el registro de carril de esta rama en sessions/status/, no borrar el test",
			before, afterOwn)
	}

	// ⛔ DIRECCIÓN 2 · CONTROL NEGATIVO. Sin él, la dirección 1 sólo dice «una escritura escribe»,
	// que es una perogrullada. Lo que se afirma es que la guarda DISTINGUE por `launch_id`: con
	// otro lanzamiento la misma llamada NO debe tocar la fila.
	// ⛔ NO se copia `*lr`: `liveRun` lleva un `sync.Mutex` y `go vet` lo caza —copiar un
	// candado es un defecto, no un detalle de estilo—. Se construye uno mínimo con lo único
	// que `mutateRunBest` mira: tenant, runRef y launchID.
	ajeno := &liveRun{tenant: lr.tenant, runRef: lr.runRef, launchID: model.NewID()}
	m.mutateRunBest(ctx, ajeno, func(rec model.Record) {
		rec[colLastActivityAt] = model.NewTimestamp(m.now()).String()
	})
	afterForeign := version()
	if afterForeign != afterOwn {
		t.Fatalf("un bridge con OTRO launch_id sí tocó la fila (%d -> %d): entonces "+
			"guardRuntimeLaunch no está fenciando nada y el hallazgo es más grave, no menos",
			afterOwn, afterForeign)
	}
}

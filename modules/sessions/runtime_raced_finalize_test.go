// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// racedFinalizeFixture creates a real run and hands back a liveRun handle whose
// finalizedCh is still OPEN — the state a compensation meets while the bridge is still
// working.
func racedFinalizeFixture(t *testing.T, m *Module, tenant model.TenantID) (string, *liveRun) {
	t.Helper()
	created, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "agent:raced", ActorKind: model.ActorAgent, AgentRef: "agent:raced",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := m.loadRun(context.Background(), tenant, created.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	return created.RunRef, &liveRun{
		tenant: tenant, runRef: created.RunRef, runID: model.ID(rec.String(model.ColID)),
		proc: &fakeProc{out: make(chan OutputFrame), stopped: make(chan struct{})},
		ring: newOutputRing(2, 1024), cancel: func() {}, finalizedCh: make(chan struct{}),
	}
}

// EL 409 DE UNA CARRERA LLEVA LA FILA, Y ESA FILA ES LA ASENTADA — NO LA DE MEDIO DESMONTAJE.\n//\n// (Este testigo exigia `err == nil` hasta la decision 1 de K2. Se INVIRTIO, no se borro:\n// ver el comentario de la asercion.)
//
// teardownLiveWithContext stops the process and revokes the credentials, but the state
// move is the BRIDGE's, on its own goroutine. Reading straight after the teardown
// therefore answered with a row that had not settled yet. Measured on 2026-08-24 against
// main aec001424 with that test's t.Parallel() restored:
//
//	winner = {RunRef:01a03363-… State:pending Reason:exit PID:<nil>} / <nil>
//
// `pending` with reason `exit` and no PID is a row in mid-flight, not the run's state.
//
// The ordering here is carried by the channel, not by a sleep: the transition commits
// BEFORE close(finalizedCh), and racedFinalizeDTO reads AFTER it, so "settled" is a
// happens-before and not a hope.
func TestARacedFinalizeAnswersTheSETTLEDRowAndNotTheOneMidTeardown(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	runRef, lr := racedFinalizeFixture(t, m, tenant)
	ctx := context.Background()

	go func() {
		// What the bridge does: move the row, THEN announce it finalized.
		if _, err := m.transition(ctx, tenant, runRef, transitionInput{
			event: "stopped", toState: stateStopped, detail: "exit",
			actor: "bridge", actorKind: "system",
		}); err != nil {
			t.Errorf("the stand-in bridge could not move the row: %v", err)
		}
		close(lr.finalizedCh)
	}()

	// ⛔ ESTA ASERCION SE HA INVERTIDO CON LA DECISION 1 DE K2, y se invierte en vez de
	// borrarse. Antes exigia `err == nil`, y ESE nil era la mentira: hacia que una carrera
	// perdida saliera por la puerta del exito (201 Created) con una fila `stopped` dentro,
	// obligando a cada cliente a mirar el cuerpo para descubrir que no se creo nada.
	//
	// Ahora la carrera contesta 409 CON LA FILA: el codigo dice «esto no es la creacion que
	// pediste» y el cuerpo dice «y esto es lo que hay». Un testigo que cambia de signo con su
	// razon escrita sigue siendo un control; uno que desaparece es una perdida silenciosa.
	_, err := m.racedFinalizeDTO(ctx, tenant, runRef, lr, true, conflictErr("raced"), nil)
	var raced *racedRunErr
	if !errors.As(err, &raced) {
		t.Fatalf("una finalizacion en carrera contesto %v: tiene que ser un 409 que LLEVE la "+
			"fila. Devolver nil manda un 201 Created por una corrida que no se creo, y el "+
			"cliente que no mire el cuerpo la da por buena.", err)
	}
	if raced.dto.State != stateStopped {
		t.Fatalf("el 409 llevaba State=%q, la fila ANTES de que el puente la asentara.\n"+
			"  Leer justo despues del desmontaje entrega una fila a medio asentar —`pending` "+
			"para una corrida que se esta desmontando— y el cuerpo del 409 deja de ser el "+
			"estado real.", raced.dto.State)
	}
	if raced.dto.RunRef == "" {
		t.Fatal("el 409 llego sin `run_ref`: un 409 pelado tira el dato que el llamante " +
			"necesita y le obliga a un segundo viaje, que es justo lo que la decision evita")
	}
}

// A CHILD THAT WOULD NOT STOP IS NOT A ROW TO REPORT, IT IS A 502.
//
// The launch-failure path already answers that way; this one used to throw the bool
// away (`teardownLive` was `_, err := teardownLiveWithContext(...)`) and report a row as
// if the estate were tidy.
func TestARacedFinalizeWhoseProcessWouldNotStopIsA502AndNotARow(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	runRef, lr := racedFinalizeFixture(t, m, tenant)
	close(lr.finalizedCh) // the wait must not be what decides this case

	dto, err := m.racedFinalizeDTO(context.Background(), tenant, runRef, lr, false, conflictErr("raced"), nil)
	if err == nil {
		t.Fatalf("an unstoppable child was reported as a tidy row: %+v", dto)
	}
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusBadGateway {
		t.Fatalf("an unstoppable child answered %v, want a 502 like the launch path does", err)
	}
	if dto.RunRef != "" {
		t.Fatalf("a row travelled with the refusal: %+v", dto)
	}
}

// AND IT MUST NOT HANG ON A BRIDGE THAT NEVER ARRIVES.
//
// Same decision stopRun already took for the same channel: past the bound, answer the
// current state honestly rather than holding the caller open.
func TestARacedFinalizeThatNeverSeesTheBridgeStillAnswers(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()),
		WithStopWaitDelay(20*time.Millisecond))
	runRef, lr := racedFinalizeFixture(t, m, tenant) // finalizedCh stays OPEN forever

	done := make(chan struct{})
	go func() {
		defer close(done)
		// El SUJETO de este testigo es la COTA, no el codigo: que una espera sin fin no
		// deje al llamante colgado. Con la decision 1 la respuesta pasa a ser un 409 que
		// lleva la fila, asi que lo que se exige es que VUELVA —y que vuelva con ese 409—,
		// no que vuelva con nil. El proposito del test sobrevive; cambia el contrato.
		_, err := m.racedFinalizeDTO(context.Background(), tenant, runRef, lr, true, conflictErr("raced"), nil)
		var raced *racedRunErr
		if !errors.As(err, &raced) {
			t.Errorf("tras la cota contesto %v: se esperaba el 409 con la fila, que es la "+
				"respuesta honesta del estado corriente cuando el puente no llega", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the compensation never returned: the wait for the bridge has no bound, " +
			"so a finalize that never arrives holds the caller open forever")
	}
}

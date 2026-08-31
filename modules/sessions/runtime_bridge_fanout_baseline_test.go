// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// ⛔ ESTOS TRES TESTIGOS SE ESCRIBEN ANTES DEL CAMBIO, Y ESO ES LA MITAD DE SU VALOR.
//
// La opción 4 de K2 —«consumir siempre, diferir sólo las escrituras de fila»— toca el bombeo
// de `bridge` (runtime_bridge.go:30-56), que por cada fotograma hace TRES cosas:
//
//	seq := lr.ring.append(...)                      (1) el ANILLO, fuente de verdad del attach
//	if lr.recordIO { recorder.Record(...) }         (2) el GRABADOR
//	if stdout { m.onStdout(...) }                   (3) las FILAS  <- lo unico que se difiere
//
// Que (1) y (2) sobrevivan intactos es **estructuralmente evidente**, y por eso hay que medirlo:
// lo evidente es justo lo que nadie comprueba. PLAN pidió testigos «probados, no citados».
//
// Un testigo escrito DESPUÉS del cambio se moldea para pasarlo. Escrito antes, mide el
// comportamiento de hoy y la opción 4 tendrá que seguir satisfaciéndolo sin tocarlo.

type countingRecorder struct {
	mu    sync.Mutex
	seqs  []int64
	final int
}

func (c *countingRecorder) Record(_ context.Context, _ model.TenantID, _ string, f RecordedFrame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seqs = append(c.seqs, f.Seq)
	return nil
}

func (c *countingRecorder) Finalize(context.Context, model.TenantID, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.final++
	return nil
}

func (c *countingRecorder) snapshot() ([]int64, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.seqs...), c.final
}

// arranca un run vivo con un proceso falso de canal SIN BÚFER, que es el punto de
// sincronización: el envío no vuelve hasta que el bombeo ha recibido el fotograma.
func newFanoutRun(t *testing.T, rec Recorder, record bool) (*Module, *fakeProc, *liveRun) {
	t.Helper()
	proc := &fakeProc{out: make(chan OutputFrame), stopped: make(chan struct{})}
	opts := []Option{
		WithRunner(&handoffRunner{proc: proc}),
		WithCredentialSource(staticCred()),
		WithLaunchGate(&spyGate{inner: LaunchDecision{Allowed: true, RecordIO: record}}),
	}
	m, _, tenant, _ := newRuntimeHarness(t, opts...)
	if rec != nil {
		m.UseRecorder(rec)
	}
	dto, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()),
		Actor:        "user:u1", ActorKind: model.ActorUser,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatalf("run %s has no live handle", dto.RunRef)
	}
	t.Cleanup(func() {
		proc.finish(0)
		select {
		case <-lr.finalizedCh:
		case <-time.After(30 * time.Second):
		}
	})
	return m, proc, lr
}

// ringWithAtLeast espera a que el anillo haya PUBLICADO n fotogramas y devuelve la
// lectura. Existe porque `proc.out` NO TIENE BUFER y eso engaña: el envío vuelve
// cuando el bombeo RECIBE el fotograma (`runtime_bridge.go:72`, `for frame := range`),
// no cuando lo PUBLICA (`:74`, `ring.append`) — y entre recibir y publicar hay un
// `m.now()`, un `Record` y un `onStdout`. Leer justo tras el último envío es leer al
// escritor a medio paso: medido, 7 de 20 corridas con `-count=20 -parallel 16` daban
// exactamente un fotograma de menos, que es lo que enrojecía `control-plane` en el
// runner y nunca en una caja ociosa.
//
// Espera por el MISMO camino que usa attach (`runtime_api.go:322`): snapshot de
// `wait()` ANTES de leer, para que un append entre lectura y select no sea un
// despertar perdido.
//
// NO es una tolerancia: quien llama sigue exigiendo == n y la secuencia 1..N sin
// huecos. Si un fotograma se PIERDE de verdad, la espera se agota y la lectura corta
// llega igual a la aserción, que falla con el mismo mensaje.
func ringWithAtLeast(r *outputRing, n int, within time.Duration) ringRead {
	deadline := time.After(within)
	for {
		wake := r.wait()
		rd := r.readFrom(0)
		if len(rd.frames) >= n {
			return rd
		}
		select {
		case <-wake:
		case <-deadline:
			return rd
		}
	}
}

// TestTheRingAndTheRecorderSeeEveryFrame — testigos T1 y T2 juntos, porque comparten corrida
// y comparar sus secuencias ENTRE SÍ es más fuerte que comprobarlas por separado.
func TestTheRingAndTheRecorderSeeEveryFrame(t *testing.T) {
	t.Parallel()

	rec := &countingRecorder{}
	_, proc, lr := newFanoutRun(t, rec, true)

	const n = 12
	for i := 0; i < n; i++ {
		proc.out <- OutputFrame{Stream: streamStderr, Data: []byte(fmt.Sprintf("frame-%02d", i))}
	}

	// T1 · EL ANILLO. Se lee por el MISMO camino que usa attach (runtime_api.go:323),
	// y se ESPERA por el mismo camino también: el envío sin búfer sólo prueba que el
	// bombeo recibió el fotograma, no que lo publicara.
	read := ringWithAtLeast(lr.ring, n, 30*time.Second)
	if len(read.frames) != n {
		t.Fatalf("el anillo tiene %d fotogramas de %d: el attach serviría una corriente "+
			"incompleta, y esa es la fuente de verdad del que se engancha", len(read.frames), n)
	}
	for i, f := range read.frames {
		if want := int64(i + 1); f.Seq != want {
			t.Fatalf("seq[%d] = %d, quería %d — la secuencia del anillo no es 1..N sin huecos: %v",
				i, f.Seq, want, read.frames)
		}
		if got, want := string(f.Data), fmt.Sprintf("frame-%02d", i); got != want {
			t.Fatalf("frames[%d] = %q, quería %q: llegaron en otro ORDEN", i, got, want)
		}
	}
	if read.gap {
		t.Fatalf("el anillo declara un hueco (dropped=%d) con sólo %d fotogramas: no debería "+
			"haber evicción a este tamaño", read.dropped, n)
	}

	// T2 · EL GRABADOR, y con los MISMOS seq que el anillo.
	seqs, _ := rec.snapshot()
	if len(seqs) != n {
		t.Fatalf("el grabador recibió %d fotogramas de %d: la evidencia gobernada de E/S "+
			"tendría un hueco que nadie declara", len(seqs), n)
	}
	for i, s := range seqs {
		if s != read.frames[i].Seq {
			t.Fatalf("grabador seq[%d]=%d pero el anillo dice %d: las dos salidas del mismo "+
				"fotograma dejaron de casar", i, s, read.frames[i].Seq)
		}
	}
}

// TestTheBridgeKeepsDrainingUnderAnUnbufferedProducer — testigo T3, y el ÚNICO de los tres que
// puede romperse de verdad.
//
// ⛔ SIN `t.Parallel()`, A PROPÓSITO: mide un TRAMO DE TIEMPO. Bajo paralelismo el número deja de
// medir el bombeo y pasa a medir la caja — la misma razón que en runtime_finalize_wait_test.go.
//
// ⛔ Y SU FALLO TIENE QUE DISTINGUIR «SE BLOQUEÓ» DE «TARDÓ». El canal es SIN BÚFER, así que si el
// bombeo dejara de drenar, el envío N+1 no vuelve nunca y el test se COLGARÍA en vez de fallar. Por
// eso el envío va contra un plazo y el mensaje dice CUÁNTOS entraron antes de pararse: un timeout
// que sólo dice «expiró» no distingue un bombeo parado de una caja lenta.
func TestTheBridgeKeepsDrainingUnderAnUnbufferedProducer(t *testing.T) {
	_, proc, lr := newFanoutRun(t, nil, false)

	const n = 24
	const porFotograma = 5 * time.Second
	entraron := 0
	for i := 0; i < n; i++ {
		select {
		case proc.out <- OutputFrame{Stream: streamStderr, Data: []byte("x")}:
			entraron++
		case <-time.After(porFotograma):
			t.Fatalf("el envío del fotograma %d no volvió en %s: con un canal SIN BÚFER eso "+
				"significa que el bombeo DEJÓ DE DRENAR — entraron %d de %d antes de pararse. "+
				"No es lentitud: es contrapresión que antes no existía",
				i, porFotograma, entraron, n)
		}
	}
	if got := len(ringWithAtLeast(lr.ring, n, 30*time.Second).frames); got != n {
		t.Fatalf("entraron los %d envíos pero el anillo tiene %d: el bombeo aceptó fotogramas "+
			"que luego no publicó", n, got)
	}
}

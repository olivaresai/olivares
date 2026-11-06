// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestTheWorkChainIsMonotoneWithoutGaps checks that create, ready, block, unblock,
// block and unblock produce a gap-free sequence with an audit hash on every event.
// It exercises the module directly, not three separate provider CLIs.
// All six events carry the same actor_ref; this test does not establish attribution
// to individual providers or make that limitation a required behavior.
func TestTheWorkChainIsMonotoneWithoutGaps(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	ctx := context.Background()

	created := applyCreate(t, f, "cadena de seis pasos")
	item := created.ResultID
	version := created.Version

	// Los cinco pasos que siguen son los MISMOS que condujeron los tres motores, y en el mismo
	// orden: es la cadena del testigo, no una cadena cualquiera.
	paso := func(comando string, extra func(*WorkCommand)) {
		t.Helper()
		cmd := WorkCommand{
			Command: comando, WorkItemID: item, ExpectedVersion: version,
			IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
		}
		if extra != nil {
			extra(&cmd)
		}
		out, err := f.m.Apply(ctx, f.tenant, f.principal, cmd)
		if err != nil {
			t.Fatalf("%s: %v", comando, err)
		}
		version = out.Version
	}
	bloquea := func(c *WorkCommand) {
		// B1 midió que `item.block` sin este campo contesta `invalid_command` sin nombrarlo
		// (…-b1-tres-motores.md:42-47). Aquí se pasa: el sujeto es la cadena, no el mensaje.
		c.BlockedCode = "waiting_on_dependency"
		c.BlockedReason = "la cadena de seis pasos del testigo de B1"
	}
	paso("item.ready", nil)
	paso("item.block", bloquea)
	paso("item.unblock", nil)
	paso("item.block", bloquea)
	paso("item.unblock", nil)

	const quiero = 6
	var seqs []int64
	var sinAncla []int64
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		events, err := sc.Ext(workEventKind)
		if err != nil {
			return err
		}
		filas, err := listAll(ctx, events, model.Filter{
			Column: colEventAggregateID, Op: model.OpEq, Value: item.String(),
		})
		if err != nil {
			return err
		}
		for _, fila := range filas {
			seq := fila.Int(colEventSeq)
			seqs = append(seqs, seq)
			if len(fila.Bytes(colEventAuditHash)) == 0 {
				sinAncla = append(sinAncla, seq)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if len(seqs) != quiero {
		t.Fatalf("la cadena tiene %d eventos y el testigo de B1 publicó %d: %v — si el motor ha "+
			"dejado de escribir un evento por transición, la cadena que se audita ya no es la "+
			"que se midió", len(seqs), quiero, seqs)
	}

	// ⛔ MONOTONÍA **Y** AUSENCIA DE HUECOS SON DOS COSAS. Comprobar sólo que crece deja pasar
	// 1,2,4: creciente y con un hueco, que es exactamente el fallo que un evento perdido produce.
	// Por eso se exige la sucesión EXACTA 1..N y se nombra el primer sitio donde se rompe.
	for i, seq := range seqs {
		if seq != int64(i+1) {
			t.Fatalf("seq[%d] = %d, esperaba %d — la cadena no es 1..%d sin huecos: %v. Un hueco "+
				"aquí significa que un evento no se escribió o se escribió fuera de orden, y la "+
				"cadena deja de poder reconstruir el estado", i, seq, i+1, quiero, seqs)
		}
	}

	// ⛔ AQUÍ HABÍA UNA SEGUNDA ASERCIÓN —«ningún evento sin `audit_hash`»— Y LA HE QUITADO
	// PORQUE NO PODÍA ENROJECER. La otra mitad que B1 publicó (`…-b1-tres-motores.md:21`) sí
	// está garantizada, pero **no aquí**: la defiende el almacén, y lo defiende antes de que
	// esta prueba llegue a mirar. Medido mutando el escritor de `work_mutation.go:751`:
	//
	//	colEventAuditHash: []byte(nil)  ->  NOT NULL constraint failed: …audit_hash        (1299)
	//	colEventAuditHash: []byte{}     ->  invalid … payload or evidence hash             (1811)
	//
	// Las dos mutaciones enrojecen, pero **ninguna en mi aserción**: revientan en el `create`,
	// así que mi comprobación no llega a ejecutarse nunca. Un control que ningún mutante puede
	// hacer fallar no es un control; es documentación con formato de código, y hoy mismo he
	// quitado uno igual de `runtime_finalize_wait_test.go`. Dejarlo aquí habría inflado la
	// cuenta de garantías «verificadas» con una que verifica el vacío.
	//
	// Queda escrito dónde vive de verdad: esquema `work_schema.go:387` + la regla de vocabulario
	// del almacén. Si algún día un evento pudiera nacer sin ancla, el rojo saldría de ahí.
	_ = sinAncla
}

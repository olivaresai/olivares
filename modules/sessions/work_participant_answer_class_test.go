// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestAnUnresolvableOwnerRefIsADecisionAndNotABlindness fija la CLASE DE RESPUESTA, que es
// distinta del código y de la redacción.
//
// `checkParticipant` colapsaba CINCO causas en `unknown("evidence_unavailable")`: el plano sin
// cablear, el store caído, un `kind` desconocido, un `owner_ref` mal formado y un participante
// inexistente. Las dos primeras son ceguera del instrumento; las tres últimas son **decisiones**
// —el motor miró y dijo que no—.
//
// ⛔ POR QUÉ IMPORTA MÁS QUE UN MENSAJE. La consola elige su pantalla de «no pude mirar» leyendo
// EL VEREDICTO (`web/src/features/work/api.ts`), no el código. Así que un `owner_ref` mal escrito
// le enseñaba al operador una pantalla que dice «el motor no pudo observar» — y «no pude mirar»
// **invita a reintentar** algo que no va a funcionar nunca, por definición.
//
// Medido el 2026-08-25 conduciendo el motor de verdad para la pata de B1: mandar `user:<uuid>` en
// vez del id pelado costó TRES sondas por este camino, mientras que los rechazos que sí nombran su
// campo costaron CERO.
//
// El principio ya estaba escrito en la casa, para el otro lado — `workkernel.go:358-360`: «an
// unwired plane is I could not look, never this session is not eligible». Esto es el mismo
// principio en la dirección que faltaba.
func TestAnUnresolvableOwnerRefIsADecisionAndNotABlindness(t *testing.T) {
	t.Parallel()

	for _, row := range []struct {
		nombre   string
		resolver WorkIdentityResolver
		quiero   string // "decision-campo" | "decision" | "ceguera"
		status   int
		code     string
		campo    string
	}{
		{
			nombre:   "owner_ref mal formado -> DECISION que nombra el campo",
			resolver: fixedWorkIdentity{err: fmt.Errorf("owner_ref %q is not an id: %w", "user:abc", store.ErrInvalidID)},
			quiero:   "decision-campo", status: http.StatusBadRequest, code: "invalid_command", campo: fldOwnerRef,
		},
		{
			nombre:   "participante inexistente -> DECISION",
			resolver: fixedWorkIdentity{err: fmt.Errorf("no such user: %w", store.ErrNotFound)},
			quiero:   "decision", status: http.StatusUnprocessableEntity, code: "owner_ineligible",
		},
		{
			nombre:   "el store no responde -> CEGUERA, y esto NO cambia",
			resolver: fixedWorkIdentity{err: errors.New("dial tcp: connection refused")},
			quiero:   "ceguera", code: "evidence_unavailable",
		},
	} {
		t.Run(row.nombre, func(t *testing.T) {
			m := New(WithWorkIdentityResolver(row.resolver))
			err := m.checkParticipant(context.Background(), model.TenantID(model.NewID()), model.NewID(), "user", "user:abc")
			we := asWorkError(err)
			if we == nil {
				t.Fatalf("no devolvió un workError: %v", err)
			}
			if we.code != row.code {
				t.Fatalf("code = %q, quería %q", we.code, row.code)
			}
			switch row.quiero {
			case "ceguera":
				if we.verdict != VerdictUnknown {
					t.Fatalf("verdict = %q, quería %q: un plano caído SÍ es «no pude mirar» y eso "+
						"no debe cambiar al arreglar el otro lado", we.verdict, VerdictUnknown)
				}
			default:
				if we.verdict == VerdictUnknown {
					t.Fatalf("verdict = %q: el motor DECIDIÓ —el dato del llamante estaba mal— y "+
						"contestar «no pude mirar» invita a un reintento que no puede funcionar", we.verdict)
				}
				if we.status != row.status {
					t.Fatalf("status = %d, quería %d", we.status, row.status)
				}
			}
			if row.quiero == "decision-campo" && we.field != row.campo {
				t.Fatalf("field = %q, quería %q: nombrar el campo es la mitad barata del arreglo",
					we.field, row.campo)
			}
		})
	}
}

// TestAnUnwiredIdentityPlaneStaysTheThirdAnswer es el CONTROL INVERSO del de arriba, y existe
// porque un arreglo que convierte TODA ceguera en decisión sería peor que el defecto: diría
// «tu dato está mal» cuando el motor de verdad no ha podido mirar.
func TestAnUnwiredIdentityPlaneStaysTheThirdAnswer(t *testing.T) {
	t.Parallel()
	m := New() // sin resolutor: el plano NO está cableado
	err := m.checkParticipant(context.Background(), model.TenantID(model.NewID()), model.NewID(), "user", "irrelevante")
	we := asWorkError(err)
	if we == nil || we.verdict != VerdictUnknown || we.code != "evidence_unavailable" {
		t.Fatalf("plano sin cablear devolvió %#v, quería la TERCERA respuesta: sin resolutor el "+
			"motor no ha mirado nada, y decir que el dato del llamante está mal sería inventarse "+
			"una decisión que nadie tomó", we)
	}
}
